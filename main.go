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

type checkContext struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

type pr struct {
	Number           int    `json:"number"`
	Title            string `json:"title"`
	IsDraft          bool   `json:"isDraft"`
	ReviewDecision   string `json:"reviewDecision"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	Commits          struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type user struct {
	Login string `json:"login"`
}

type prFile struct {
	Filename string `json:"filename"`
}

type filesResult struct {
	number int
	files  []prFile
	err    error
}

type crossConflict struct {
	file string
	prs  []pr
}

type checkSummary struct {
	ok      int
	failed  []string
	pending []string
}

const searchQuery = `
query($q: String!, $after: String) {
  search(query: $q, type: ISSUE, first: 100, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        title
        isDraft
        reviewDecision
        mergeable
        mergeStateStatus
        commits(last: 1) {
          nodes {
            commit {
              statusCheckRollup {
                contexts(first: 100) {
                  nodes {
                    __typename
                    ... on CheckRun { name status conclusion }
                    ... on StatusContext { context state }
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`

type searchResponse struct {
	Search struct {
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []pr `json:"nodes"`
	} `json:"search"`
}

// searchPRs runs the search query, following pagination. Asking for mergeStateStatus
// also triggers GitHub to compute mergeability for PRs whose cache is cold.
func searchPRs(client *api.GraphQLClient, q string) ([]pr, error) {
	var prs []pr
	var after *string
	for {
		var resp searchResponse
		vars := map[string]interface{}{"q": q, "after": after}
		if err := client.Do(searchQuery, vars, &resp); err != nil {
			return nil, err
		}
		prs = append(prs, resp.Search.Nodes...)
		if !resp.Search.PageInfo.HasNextPage {
			break
		}
		cursor := resp.Search.PageInfo.EndCursor
		after = &cursor
	}
	return prs, nil
}

func isUnknown(p pr) bool {
	return p.Mergeable == "UNKNOWN" || p.MergeStateStatus == "UNKNOWN" || p.MergeStateStatus == ""
}

func summarizeChecks(p pr) checkSummary {
	var s checkSummary
	if len(p.Commits.Nodes) == 0 || p.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return s
	}
	for _, c := range p.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes {
		name, outcome := c.Name, c.Conclusion
		switch c.Typename {
		case "CheckRun":
			if c.Status != "COMPLETED" {
				outcome = "PENDING"
			}
		case "StatusContext":
			name, outcome = c.Context, c.State
		}
		switch outcome {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			s.ok++
		case "PENDING", "EXPECTED", "":
			s.pending = append(s.pending, name)
		default:
			s.failed = append(s.failed, name)
		}
	}
	return s
}

func (s checkSummary) String() string {
	total := s.ok + len(s.failed) + len(s.pending)
	switch {
	case total == 0:
		return "no checks"
	case s.ok == total:
		return fmt.Sprintf("%d/%d checks passed", s.ok, total)
	}
	parts := []string{fmt.Sprintf("%d ok", s.ok)}
	if len(s.failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", len(s.failed)))
	}
	if len(s.pending) > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", len(s.pending)))
	}
	return strings.Join(parts, ", ")
}

// stateLabel maps GitHub's mergeStateStatus to a display label and color.
func stateLabel(p pr) (string, string) {
	if p.IsDraft || p.MergeStateStatus == "DRAFT" {
		return "DRAFT", "\033[2m"
	}
	switch p.MergeStateStatus {
	case "CLEAN", "HAS_HOOKS":
		return "READY", "\033[32m"
	case "DIRTY":
		return "CONFLICT", "\033[31m"
	case "BLOCKED":
		return "BLOCKED", "\033[31m"
	case "BEHIND":
		return "BEHIND", "\033[33m"
	case "UNSTABLE":
		return "UNSTABLE", "\033[33m"
	default:
		return "UNKNOWN", "\033[33m"
	}
}

func fetchPRFiles(client *api.RESTClient, owner, name string, numbers []int) []filesResult {
	sem := make(chan struct{}, 8)
	results := make([]filesResult, len(numbers))
	var wg sync.WaitGroup

	for i, num := range numbers {
		wg.Add(1)
		go func(i, num int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var allFiles []prFile
			for page := 1; ; page++ {
				var pageFiles []prFile
				path := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100&page=%d", owner, name, num, page)
				if err := client.Get(path, &pageFiles); err != nil {
					results[i] = filesResult{number: num, err: err}
					return
				}
				allFiles = append(allFiles, pageFiles...)
				if len(pageFiles) < 100 {
					break
				}
			}
			results[i] = filesResult{number: num, files: allFiles}
		}(i, num)
	}
	wg.Wait()
	return results
}

func findCrossConflicts(prs []pr, filesByPR map[int][]prFile) []crossConflict {
	prByNum := make(map[int]pr, len(prs))
	for _, p := range prs {
		prByNum[p.Number] = p
	}

	filePRNums := make(map[string][]int)
	for _, p := range prs {
		for _, f := range filesByPR[p.Number] {
			filePRNums[f.Filename] = append(filePRNums[f.Filename], p.Number)
		}
	}

	var conflicts []crossConflict
	for file, nums := range filePRNums {
		if len(nums) < 2 {
			continue
		}
		cPRs := make([]pr, 0, len(nums))
		for _, num := range nums {
			cPRs = append(cPRs, prByNum[num])
		}
		sort.Slice(cPRs, func(i, j int) bool {
			return cPRs[i].Number > cPRs[j].Number
		})
		conflicts = append(conflicts, crossConflict{file: file, prs: cPRs})
	}

	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].file < conflicts[j].file
	})

	return conflicts
}

func main() {
	repoFlag := flag.String("repo", "", "owner/repo (auto-detected from git remote if omitted)")
	authorFlag := flag.String("author", "", "GitHub username to filter by (defaults to authenticated user)")
	approvedOnly := flag.Bool("approved", false, "only check approved PRs; exit 1 if any is not ready to merge")
	readyOnly := flag.Bool("ready-only", false, "only show PRs that are ready to merge")
	conflictsOnly := flag.Bool("conflicts-only", false, "only show PRs with merge conflicts")
	crossPR := flag.Bool("cross-pr", false, "also check for file overlaps between open PRs")
	flag.Parse()

	client, err := api.DefaultRESTClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	gqlClient, err := api.DefaultGraphQLClient()
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

	q := fmt.Sprintf("repo:%s/%s is:pr is:open author:%s", repoOwner, repoName, author)
	kind := "open"
	if *approvedOnly {
		q += " review:approved"
		kind = "approved"
	}

	// GitHub computes mergeability asynchronously and returns UNKNOWN when the cache is
	// cold. The first query triggers the computation; retry up to 3 times while any PR
	// is still UNKNOWN.
	var prs []pr
	for attempt := 0; attempt < 4; attempt++ {
		prs, err = searchPRs(gqlClient, q)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error searching PRs:", err)
			os.Exit(1)
		}
		pending := 0
		for _, p := range prs {
			if isUnknown(p) {
				pending++
			}
		}
		if pending == 0 || attempt == 3 {
			break
		}
		fmt.Fprintf(os.Stderr, "%d PR(s) still computing mergeability, retrying...\n", pending)
		time.Sleep(3 * time.Second)
	}

	fmt.Printf("Checking %d %s PRs in %s/%s by %s...\n\n", len(prs), kind, repoOwner, repoName, author)

	sort.Slice(prs, func(i, j int) bool {
		return prs[i].Number > prs[j].Number
	})

	isTerminal := term.IsTerminal(os.Stdout)
	conflicts, ready := 0, 0

	for _, p := range prs {
		label, color := stateLabel(p)
		switch label {
		case "READY":
			ready++
		case "CONFLICT":
			conflicts++
		}

		if *conflictsOnly && label != "CONFLICT" {
			continue
		}
		if *readyOnly && label != "READY" {
			continue
		}

		review := p.ReviewDecision
		if review == "" {
			review = "-"
		}
		checks := summarizeChecks(p)

		title := p.Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		fmt.Printf("#%-5d %s  %-17s  %-26s  %s\n", p.Number,
			colorize(fmt.Sprintf("%-8s", label), color, isTerminal), review, checks, title)

		if label != "READY" && (len(checks.failed) > 0 || len(checks.pending) > 0) {
			var detail []string
			if len(checks.failed) > 0 {
				detail = append(detail, colorize("failed: ", "\033[31m", isTerminal)+strings.Join(checks.failed, ", "))
			}
			if len(checks.pending) > 0 {
				detail = append(detail, colorize("pending: ", "\033[33m", isTerminal)+strings.Join(checks.pending, ", "))
			}
			fmt.Printf("       %s\n", strings.Join(detail, "   "))
		}
	}

	fmt.Printf("\n%d of %d %s PRs ready to merge, %d conflict(s)\n", ready, len(prs), kind, conflicts)

	crossConflicts := 0
	if *crossPR && len(prs) > 1 {
		fmt.Println()
		fmt.Println("Checking for cross-PR file overlaps...")

		numbers := make([]int, len(prs))
		for i, p := range prs {
			numbers[i] = p.Number
		}
		fileResults := fetchPRFiles(client, repoOwner, repoName, numbers)
		filesByPR := make(map[int][]prFile, len(prs))
		for _, fr := range fileResults {
			if fr.err != nil {
				fmt.Fprintf(os.Stderr, "error fetching files for PR #%d: %v\n", fr.number, fr.err)
				continue
			}
			filesByPR[fr.number] = fr.files
		}

		overlaps := findCrossConflicts(prs, filesByPR)
		if len(overlaps) == 0 {
			fmt.Println("\nNo cross-PR file overlaps found.")
		} else {
			crossConflicts = len(overlaps)
			fmt.Printf("\n%d file(s) shared across PRs:\n", crossConflicts)
			for _, c := range overlaps {
				fmt.Printf("\n  %s\n", c.file)
				for _, p := range c.prs {
					title := p.Title
					if len(title) > 60 {
						title = title[:57] + "..."
					}
					fmt.Printf("    #%-5d  %s\n", p.Number, title)
				}
			}
			fmt.Println()
		}
	}

	if conflicts > 0 || crossConflicts > 0 || (*approvedOnly && ready < len(prs)) {
		os.Exit(1)
	}
}

func colorize(s, code string, enabled bool) string {
	if !enabled {
		return s
	}
	return code + s + "\033[0m"
}
