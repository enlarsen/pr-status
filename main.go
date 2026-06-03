package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/cli/go-gh/v2/pkg/term"
)

type pr struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
}

type user struct {
	Login string `json:"login"`
}

type result struct {
	pr  pr
	err error
}

func fetchPRs(client *api.RESTClient, owner, name string, numbers []int) []result {
	sem := make(chan struct{}, 8)
	results := make([]result, len(numbers))
	var wg sync.WaitGroup

	for i, num := range numbers {
		wg.Add(1)
		go func(i, num int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var p pr
			path := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, name, num)
			err := client.Get(path, &p)
			results[i] = result{pr: p, err: err}
		}(i, num)
	}
	wg.Wait()
	return results
}

func main() {
	repoFlag := flag.String("repo", "", "owner/repo (auto-detected from git remote if omitted)")
	authorFlag := flag.String("author", "", "GitHub username to filter by (defaults to authenticated user)")
	conflictsOnly := flag.Bool("conflicts-only", false, "only show PRs with merge conflicts")
	flag.Parse()

	client, err := api.DefaultRESTClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	var repoOwner, repoName string
	if *repoFlag != "" {
		r, err := repository.Parse(*repoFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error parsing -repo:", err)
			os.Exit(1)
		}
		repoOwner, repoName = r.Owner, r.Name
	} else {
		r, err := repository.Current()
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not detect repo from git remote; use -repo owner/repo")
			os.Exit(1)
		}
		repoOwner, repoName = r.Owner, r.Name
	}

	author := *authorFlag
	if author == "" {
		var u user
		if err := client.Get("user", &u); err != nil {
			fmt.Fprintln(os.Stderr, "error fetching authenticated user:", err)
			os.Exit(1)
		}
		author = u.Login
	}

	// List all open PRs, filter by author client-side (the pulls API has no author filter).
	var prs []pr
	for page := 1; ; page++ {
		var pagePRs []pr
		path := fmt.Sprintf("repos/%s/%s/pulls?state=open&per_page=100&page=%d", repoOwner, repoName, page)
		if err := client.Get(path, &pagePRs); err != nil {
			fmt.Fprintln(os.Stderr, "error listing PRs:", err)
			os.Exit(1)
		}
		for _, p := range pagePRs {
			if strings.EqualFold(p.User.Login, author) {
				prs = append(prs, p)
			}
		}
		if len(pagePRs) < 100 {
			break
		}
	}

	fmt.Printf("Checking %d open PRs in %s/%s by %s...\n\n", len(prs), repoOwner, repoName, author)

	// Fetch each PR individually — this triggers GitHub to compute the mergeable field.
	// GitHub computes mergeability asynchronously and returns null on the first call when the
	// cache is cold. We retry UNKNOWN results up to 3 times with a short wait between attempts.
	numbers := make([]int, len(prs))
	for i, p := range prs {
		numbers[i] = p.Number
	}

	byNumber := make(map[int]result, len(prs))
	pending := numbers

	for attempt := 0; len(pending) > 0 && attempt < 4; attempt++ {
		if attempt > 0 {
			fmt.Fprintf(os.Stderr, "%d PR(s) still computing mergeability, retrying...\n", len(pending))
			time.Sleep(3 * time.Second)
		}

		got := fetchPRs(client, repoOwner, repoName, pending)
		pending = pending[:0]
		for _, r := range got {
			if r.err != nil {
				byNumber[r.pr.Number] = r
				continue
			}
			byNumber[r.pr.Number] = r
			if r.pr.Mergeable == nil {
				pending = append(pending, r.pr.Number)
			}
		}
	}

	results := make([]result, 0, len(prs))
	for _, p := range prs {
		results = append(results, byNumber[p.Number])
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].pr.Number > results[j].pr.Number
	})

	isTerminal := term.IsTerminal(os.Stdout)
	conflicts := 0

	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "error fetching PR #%d: %v\n", r.pr.Number, r.err)
			continue
		}

		var label string
		switch {
		case r.pr.Mergeable == nil:
			label = colorize("UNKNOWN ", "\033[33m", isTerminal)
		case *r.pr.Mergeable:
			label = colorize("CLEAN   ", "\033[32m", isTerminal)
		default:
			label = colorize("CONFLICT", "\033[31m", isTerminal)
			conflicts++
		}

		if *conflictsOnly && (r.pr.Mergeable == nil || *r.pr.Mergeable) {
			continue
		}

		title := r.pr.Title
		if len(title) > 68 {
			title = title[:65] + "..."
		}
		fmt.Printf("#%-5d %s  %s\n", r.pr.Number, label, title)
	}

	fmt.Printf("\n%d conflict(s) among %d open PRs\n", conflicts, len(prs))
	if conflicts > 0 {
		os.Exit(1)
	}
}

func colorize(s, code string, enabled bool) string {
	if !enabled {
		return s
	}
	return code + s + "\033[0m"
}
