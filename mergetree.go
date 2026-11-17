package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
)

// mergeResult is the outcome of a trial merge done with `git merge-tree`, which merges
// in memory without touching the working tree or index.
type mergeResult struct {
	tree       string   // resulting tree (written even when there are conflicts)
	conflicted []string // files with conflicts; empty means a clean merge
}

func trialMerge(dir, a, b string) (mergeResult, error) {
	cmd := exec.Command("git", "-C", dir, "merge-tree", "--write-tree", "--name-only", "--no-messages", a, b)
	out, err := cmd.Output()
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var exit *exec.ExitError
	switch {
	case err == nil:
		return mergeResult{tree: lines[0]}, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return mergeResult{tree: lines[0], conflicted: lines[1:]}, nil
	default:
		msg := ""
		if exit != nil {
			msg = strings.TrimSpace(string(exit.Stderr))
		}
		return mergeResult{}, fmt.Errorf("git merge-tree %s %s: %v %s", a, b, err, msg)
	}
}

// landedCommit simulates the PR having merged into base: a throwaway commit whose tree
// is base+PR. Merging the branch against it answers "once #N lands, will mine still
// merge?" without blaming the branch for drift between the two PRs' merge bases. It
// returns ok=false if the PR already conflicts with base, since GitHub reports that on
// the PR itself and there is nothing meaningful to stack on.
func landedCommit(dir, base, prRef string) (string, bool, error) {
	mr, err := trialMerge(dir, base, prRef)
	if err != nil {
		return "", false, err
	}
	if len(mr.conflicted) > 0 {
		return "", false, nil
	}
	c, err := git(dir, "commit-tree", mr.tree, "-p", base, "-p", prRef, "-m", "pr-status trial merge")
	return c, err == nil, err
}

// runAgainst is the pre-push mode: would ref merge cleanly with base, and with each
// open PR that shares a file with it? Returns the number of real conflicts found.
func runAgainst(gql *api.GraphQLClient, rest *api.RESTClient, owner, name, dir, ref string, includeBulk bool) int {
	die := func(format string, a ...interface{}) {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
		os.Exit(2)
	}
	if _, err := git(dir, "rev-parse", "--git-dir"); err != nil {
		die("-against needs a local clone: %s is not a git repository (use -C <dir>)", dir)
	}
	head, err := git(dir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		die("cannot resolve %q in %s", ref, dir)
	}
	branch, _ := git(dir, "rev-parse", "--abbrev-ref", ref)

	baseBranch := "master"
	if b, err := git(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		baseBranch = strings.TrimPrefix(b, "origin/")
	}
	if _, err := git(dir, "fetch", "--quiet", "origin", baseBranch); err != nil {
		die("git fetch origin %s failed", baseBranch)
	}
	base := "origin/" + baseBranch

	changedOut, err := git(dir, "diff", "--name-only", base+"..."+head)
	if err != nil {
		die("git diff against %s failed", base)
	}
	if changedOut == "" {
		fmt.Printf("%s has no changes relative to %s.\n", ref, base)
		return 0
	}
	changed := strings.Split(changedOut, "\n")
	changedSet := map[string]bool{}
	for _, c := range changed {
		changedSet[c] = true
	}

	prs, skipped, err := listOpenPRs(gql, rest, owner, name, includeBulk)
	if err != nil {
		die("error listing open PRs: %v", err)
	}

	fmt.Printf("Trial-merging %s (%d changed file(s)) in %s/%s...\n", ref, len(changed), owner, name)
	if len(skipped) > 0 {
		fmt.Printf("Skipped bulk PRs %s (use -include-bulk)\n", joinNums(skipped))
	}
	fmt.Println()

	conflicts := 0
	mr, err := trialMerge(dir, base, head)
	switch {
	case err != nil:
		die("%v", err)
	case len(mr.conflicted) > 0:
		conflicts++
		fmt.Printf("  %-12s CONFLICT  %s\n", base, strings.Join(mr.conflicted, ", "))
	default:
		fmt.Printf("  %-12s clean\n", base)
	}

	type candidate struct {
		pr     openPR
		shared []string
	}
	var cands []candidate
	for _, p := range prs {
		if p.HeadRefOid == head || (branch != "" && branch != "HEAD" && p.HeadRefName == branch) {
			continue // the branch's own PR
		}
		var shared []string
		for _, f := range p.paths {
			if changedSet[f] {
				shared = append(shared, f)
			}
		}
		if len(shared) > 0 {
			cands = append(cands, candidate{p, shared})
		}
	}
	if len(cands) == 0 {
		fmt.Println("\nNo other open PR touches the same files.")
		return conflicts
	}

	// Fetch only the PR heads we need, into a private ref namespace.
	fetchArgs := []string{"fetch", "--quiet", "--no-tags", "origin"}
	for _, c := range cands {
		fetchArgs = append(fetchArgs, fmt.Sprintf("+refs/pull/%d/head:refs/pr-status/pull/%d", c.pr.Number, c.pr.Number))
	}
	if _, err := git(dir, fetchArgs...); err != nil {
		die("fetching PR heads failed: %v", err)
	}

	var stale []int
	for _, c := range cands {
		prRef := fmt.Sprintf("refs/pr-status/pull/%d", c.pr.Number)
		label := prLabel(c.pr)
		landed, ok, err := landedCommit(dir, base, prRef)
		if err != nil {
			fmt.Printf("  %s\n      error: %v\n", label, err)
			continue
		}
		if !ok {
			// The PR conflicts with base already, so it can't land as-is and there is
			// no honest "after it lands" state to test against. A direct pairwise merge
			// would count every base change since that PR branched as if this branch
			// had made it, reporting conflicts in files this branch never touched.
			// That PR has to be rebased first; it's checked properly once it is.
			stale = append(stale, c.pr.Number)
			fmt.Printf("  %s\n      skipped: already conflicts with %s, so it must be rebased before it can land (shares: %s)\n", label, base, strings.Join(c.shared, ", "))
			continue
		}
		r, err := trialMerge(dir, head, landed)
		if err != nil {
			fmt.Printf("  %s\n      error: %v\n", label, err)
			continue
		}
		if len(r.conflicted) > 0 {
			conflicts++
			fmt.Printf("  %s\n      CONFLICT: %s\n", label, strings.Join(r.conflicted, ", "))
		} else {
			fmt.Printf("  %s\n      same file(s), merges cleanly: %s\n", label, strings.Join(c.shared, ", "))
		}
	}
	fmt.Println()
	if len(stale) > 0 {
		fmt.Printf("Skipped %d PR(s) that already conflict with %s: %s. Not counted; rerun once they're rebased.\n", len(stale), base, joinNums(stale))
	}
	if conflicts > 0 {
		fmt.Printf("%d real conflict(s). Whichever PR merges second will need a rebase.\n", conflicts)
	} else {
		fmt.Println("No textual conflicts. Shared files may still overlap in meaning; skim the other PR's sections.")
	}
	return conflicts
}
