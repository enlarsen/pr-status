package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/cli/go-gh/v2/pkg/api"
)

// crossEntry is one PR taking part in the cross-PR comparison.
type crossEntry struct {
	number int
	label  string
	mine   bool
}

// overlapSlack is how close two changed ranges may sit before they count as touching.
// Git won't auto-merge adjacent hunks, and the PRs' bases can differ by a few lines.
const overlapSlack = 3

// runCrossPR reports files touched by more than one open PR, and whether the PRs'
// changes in each file actually come near each other. mine is the author's PRs from
// the status table; with allAuthors, every other open PR is compared against them too
// (pairs of two other people's PRs are not reported). Returns the number of files
// where changed lines overlap.
func runCrossPR(gql *api.GraphQLClient, rest *api.RESTClient, owner, name string, mine []pr, allAuthors, includeBulk bool) int {
	entries := map[int]crossEntry{}
	for _, p := range mine {
		entries[p.Number] = crossEntry{p.Number, shortTitle(fmt.Sprintf("#%-5d  %s", p.Number, p.Title)), true}
	}
	if allAuthors {
		others, _, err := listOpenPRs(gql, rest, owner, name, includeBulk)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error listing open PRs:", err)
		}
		for _, o := range others {
			if _, ok := entries[o.Number]; !ok {
				entries[o.Number] = crossEntry{o.Number, prLabel(o), false}
			}
		}
	}
	if len(entries) < 2 {
		return 0
	}

	fmt.Println()
	if allAuthors {
		fmt.Println("Checking for overlaps between your PRs and all open PRs...")
	} else {
		fmt.Println("Checking for cross-PR overlaps...")
	}

	numbers := make([]int, 0, len(entries))
	for n := range entries {
		numbers = append(numbers, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(numbers)))

	// Patches per PR per file, from the REST files endpoint.
	patches := map[int]map[string]string{}
	for _, fr := range fetchPRFiles(rest, owner, name, numbers) {
		if fr.err != nil {
			fmt.Fprintf(os.Stderr, "error fetching files for PR #%d: %v\n", fr.number, fr.err)
			continue
		}
		m := map[string]string{}
		for _, f := range fr.files {
			m[f.Filename] = f.Patch
		}
		patches[fr.number] = m
	}

	byFile := map[string][]int{}
	for _, n := range numbers {
		for f := range patches[n] {
			byFile[f] = append(byFile[f], n)
		}
	}
	var files []string
	for f, nums := range byFile {
		if len(nums) < 2 {
			continue
		}
		hasMine := false
		for _, n := range nums {
			if entries[n].mine {
				hasMine = true
			}
		}
		if hasMine {
			sort.Sort(sort.Reverse(sort.IntSlice(nums)))
			files = append(files, f)
		}
	}
	sort.Strings(files)

	if len(files) == 0 {
		fmt.Println("\nNo cross-PR file overlaps found.")
		return 0
	}

	risky := 0
	fmt.Printf("\n%d file(s) shared across PRs:\n", len(files))
	for _, f := range files {
		nums := byFile[f]
		changes := map[int]hunkChange{}
		for _, n := range nums {
			changes[n] = parsePatch(patches[n][f])
		}
		overlap := false
		for i := 0; i < len(nums) && !overlap; i++ {
			for j := i + 1; j < len(nums); j++ {
				a, b := nums[i], nums[j]
				if !entries[a].mine && !entries[b].mine {
					continue
				}
				// A missing patch (binary or huge diff) can't be ruled out.
				if patches[a][f] == "" || patches[b][f] == "" || rangesOverlap(changes[a].Ranges, changes[b].Ranges, overlapSlack) {
					overlap = true
					break
				}
			}
		}
		verdict := "different lines, likely merges cleanly"
		if overlap {
			risky++
			verdict = "OVERLAPPING LINES, likely conflict"
		}
		fmt.Printf("\n  %s  (%s)\n", f, verdict)
		for _, n := range nums {
			fmt.Printf("    %s\n        %s\n", entries[n].label, describeChange(changes[n], nil))
		}
	}
	fmt.Printf("\n%d of %d shared file(s) have overlapping lines. Line numbers are estimates when the PRs branched from different commits; use -against for a definitive check.\n\n", risky, len(files))
	return risky
}

func shortTitle(s string) string {
	if len(s) > 68 {
		return s[:65] + "..."
	}
	return s
}
