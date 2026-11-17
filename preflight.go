package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// openPR is an open PR from any author, with its changed paths.
type openPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	IsDraft     bool   `json:"isDraft"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
	Files struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	} `json:"files"`
	paths []string
}

const openPRsQuery = `
query($q: String!, $after: String) {
  search(query: $q, type: ISSUE, first: 25, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number title isDraft headRefName headRefOid
        author { login }
        files(first: 100) { totalCount nodes { path } }
      }
    }
  }
}`

type openPRsResponse struct {
	Search struct {
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []openPR `json:"nodes"`
	} `json:"search"`
}

// bulkBranch reports whether a PR is a machine-generated bulk change (the rolling
// translation PR, the develop-to-master sync) that touches everything and would
// otherwise drown the report.
func bulkBranch(head string) bool {
	return strings.HasPrefix(head, "chore/translate/") || head == "sync-develop-master"
}

// listOpenPRs returns every open PR in the repo with its full list of changed paths.
// PRs with more than 100 files fall back to the paginated REST endpoint; bulk PRs are
// dropped unless includeBulk is set.
func listOpenPRs(gql *api.GraphQLClient, rest *api.RESTClient, owner, name string, includeBulk bool) ([]openPR, []int, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr is:open", owner, name)
	var prs []openPR
	var skipped []int
	var after *string
	for {
		// Asking for every PR's file list is heavy enough that GitHub occasionally
		// answers 502; retry a couple of times before giving up.
		var resp openPRsResponse
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if err = gql.Do(openPRsQuery, map[string]interface{}{"q": q, "after": after}, &resp); err == nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
		}
		if err != nil {
			return nil, nil, err
		}
		for _, p := range resp.Search.Nodes {
			if !includeBulk && bulkBranch(p.HeadRefName) {
				skipped = append(skipped, p.Number)
				continue
			}
			prs = append(prs, p)
		}
		if !resp.Search.PageInfo.HasNextPage {
			break
		}
		c := resp.Search.PageInfo.EndCursor
		after = &c
	}

	var big []int
	for i := range prs {
		if prs[i].Files.TotalCount > len(prs[i].Files.Nodes) {
			big = append(big, prs[i].Number)
			continue
		}
		for _, f := range prs[i].Files.Nodes {
			prs[i].paths = append(prs[i].paths, f.Path)
		}
	}
	if len(big) > 0 {
		byNum := map[int]*openPR{}
		for i := range prs {
			byNum[prs[i].Number] = &prs[i]
		}
		for _, fr := range fetchPRFiles(rest, owner, name, big) {
			if fr.err != nil {
				fmt.Fprintf(os.Stderr, "error fetching files for PR #%d: %v\n", fr.number, fr.err)
				continue
			}
			for _, f := range fr.files {
				byNum[fr.number].paths = append(byNum[fr.number].paths, f.Filename)
			}
		}
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number > prs[j].Number })
	return prs, skipped, nil
}

// fetchPatch returns the unified diff GitHub holds for one file of one PR, or "" when
// GitHub omits it (binary files, very large diffs).
func fetchPatch(rest *api.RESTClient, owner, name string, num int, path string) (string, error) {
	for page := 1; ; page++ {
		var files []prFile
		p := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100&page=%d", owner, name, num, page)
		if err := rest.Get(p, &files); err != nil {
			return "", err
		}
		for _, f := range files {
			if f.Filename == path {
				return f.Patch, nil
			}
		}
		if len(files) < 100 {
			return "", nil
		}
	}
}

// git runs a git command in dir and returns trimmed stdout.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

// baseHeadings reads path at ref from the local clone and parses its headings. It
// returns nil (fall back to line ranges) for non-markdown files or when the clone or
// file isn't available.
func baseHeadings(dir, ref, path string) []heading {
	if dir == "" || !strings.HasSuffix(path, ".md") {
		return nil
	}
	content, err := git(dir, "show", ref+":"+path)
	if err != nil {
		return nil
	}
	return parseHeadings(content)
}

func prLabel(p openPR) string {
	draft := ""
	if p.IsDraft {
		draft = "[DRAFT] "
	}
	title := p.Title
	if len(title) > 50 {
		title = title[:47] + "..."
	}
	return fmt.Sprintf("#%-5d %s(%s) %s", p.Number, draft, p.Author.Login, title)
}

// runFiles is the pre-flight mode: for each path, which open PRs touch it and which
// sections. Returns the number of paths with at least one hit.
func runFiles(gql *api.GraphQLClient, rest *api.RESTClient, owner, name, dir string, paths []string, includeBulk bool, exclude map[int]bool) int {
	prs, skipped, err := listOpenPRs(gql, rest, owner, name, includeBulk)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error listing open PRs:", err)
		os.Exit(2)
	}
	fmt.Printf("Checking %d path(s) against %d open PRs in %s/%s (all authors)", len(paths), len(prs), owner, name)
	if len(skipped) > 0 {
		fmt.Printf("; skipped bulk PRs %s (use -include-bulk)", joinNums(skipped))
	}
	fmt.Print("...\n")

	base := "origin/master"
	if dir != "" {
		if b, err := git(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && b != "" {
			base = b
		}
	}

	hitPaths := 0
	for _, path := range paths {
		var touching []openPR
		for _, p := range prs {
			if exclude[p.Number] {
				continue
			}
			for _, f := range p.paths {
				if f == path {
					touching = append(touching, p)
					break
				}
			}
		}
		fmt.Printf("\n  %s\n", path)
		if dir != "" {
			if _, err := git(dir, "cat-file", "-e", base+":"+path); err != nil {
				fmt.Printf("    note: not on %s (new file, or a mistyped path?)\n", base)
			}
		}
		if len(touching) == 0 {
			fmt.Println("    no open PR touches this file")
			continue
		}
		hitPaths++
		hs := baseHeadings(dir, base, path)
		for _, p := range touching {
			desc := "(diff unavailable)"
			if patch, err := fetchPatch(rest, owner, name, p.Number, path); err != nil {
				fmt.Fprintf(os.Stderr, "error fetching patch for #%d: %v\n", p.Number, err)
			} else if patch != "" {
				desc = describeChange(parsePatch(patch), hs)
			}
			fmt.Printf("    %s\n        %s\n", prLabel(p), desc)
		}
	}
	fmt.Println()
	if hitPaths > 0 {
		fmt.Printf("%d of %d path(s) already touched by open PRs. Consider folding into one of them, or coordinate merge order.\n", hitPaths, len(paths))
	} else {
		fmt.Println("No open PR touches these paths.")
	}
	return hitPaths
}

func joinNums(nums []int) string {
	s := make([]string, len(nums))
	for i, n := range nums {
		s[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(s, ", ")
}
