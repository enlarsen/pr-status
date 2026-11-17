# pr-status

A command-line tool that tells you whether your open GitHub pull requests can be merged: merge conflicts, review decision, branch-protection state, and CI check results in one table. It also warns you about overlap with other open PRs: before you start editing (`-files`), before you push (`-against`), and across PRs that are already open (`-cross-pr`). (Formerly `pr-conflicts`, which only reported conflicts.)

GitHub computes mergeability lazily and asynchronously, so a first query often returns `UNKNOWN`. This tool asks for it (which triggers the computation) and retries while any PR is still pending, so you get a definitive answer rather than a list of unknowns.

## Installation

```
go install github.com/enlarsen/pr-status@latest
```

Make sure `$(go env GOPATH)/bin` is in your `PATH`.

## Usage

Run from inside any GitHub repository to check your own open PRs:

```
pr-status
```

Or specify a repository explicitly:

```
pr-status -repo owner/repo
```

```
Checking 4 approved PRs in owner/repo by octocat...

#4485  READY     APPROVED           4/4 checks passed           Add release notes
#4437  UNSTABLE  APPROVED           2 ok, 1 failed, 1 pending   Fix formatting
       failed: Web Preview   pending: Security Review

1 of 2 approved PRs ready to merge, 0 conflict(s)
```

### States

The state column is GitHub's `mergeStateStatus`:

| State | Meaning |
|-------|---------|
| `READY` | Mergeable: reviews and required checks satisfied |
| `UNSTABLE` | Mergeable, but non-required checks are failing or pending |
| `BLOCKED` | Branch protection blocks the merge (missing review or failing required check) |
| `BEHIND` | The branch must be updated with the base branch first |
| `CONFLICT` | The branch has merge conflicts |
| `DRAFT` | The PR is a draft |
| `UNKNOWN` | GitHub still hadn't computed mergeability after retrying |

Rows that are not `READY` get a second line naming their failed and pending checks.

### Flags

| Flag | Description |
|------|-------------|
| `-repo owner/repo` | Repository to check (auto-detected from `git remote` if omitted) |
| `-author username` | GitHub user whose PRs to check (defaults to the authenticated user) |
| `-approved` | Only check approved PRs |
| `-ready-only` | Only show PRs that are ready to merge |
| `-conflicts-only` | Only show PRs with merge conflicts |
| `-cross-pr` | Also report files touched by more than one open PR, and whether the changed lines overlap |
| `-all-authors` | With `-cross-pr`, compare your PRs against every open PR, not just each other |
| `-files <path>...` | Pre-flight: list open PRs (any author, drafts included) that touch these paths, by section |
| `-against <ref>` | Pre-push: trial-merge a local branch against the base branch and every open PR that shares a file with it |
| `-C <dir>` | Local clone used by `-files` (section names) and `-against` (trial merges). Defaults to `.` |
| `-include-bulk` | Include bulk PRs: branches under `chore/translate/` and `sync-develop-master`. Skipped by default because they touch everything |

### Before you edit: `-files`

Run this when you know which files a change will touch, before creating a branch. It lists every open PR, from any author and including drafts, that already changes those files, and names the sections each one changes (for Markdown, using the headings on the base branch in your local clone; otherwise line ranges). `+§` marks a heading the PR adds, `-§` one it removes.

```
pr-status -files packages/developer-hub/content/2/en/get-started/dh-understand-results.md

  packages/developer-hub/content/2/en/get-started/dh-understand-results.md
    #4795  [DRAFT] (octocat) Document the redesigned issues list
        §Learn More About Accessibility Issues, -§Comparison Table, §Rules List, +§Issue Groups
```

This is the moment to choose: fold your change into that PR, make yours separate and agree a merge order, or wait. Once both PRs exist and one has been approved, every push to resolve the overlap resets the approval.

A path that doesn't exist on the base branch gets a note, so a mistyped path doesn't pass as "no PR touches this file".

### Before you push: `-against`

```
pr-status -against HEAD
```

This trial-merges your branch with `git merge-tree`, in memory, without touching your working tree. First against the base branch, then against each open PR that shares a file with it, simulating "that PR has landed, then mine merges". The result is exact rather than an estimate:

```
  origin/master clean
  #4795  [DRAFT] (octocat) Document the redesigned issues list
      CONFLICT: packages/developer-hub/content/2/en/get-started/dh-understand-results.md
  #4730  (octocat) Replace the options table
      same file(s), merges cleanly: packages/.../wa-results-cli.md
```

If the other PR already conflicts with the base branch, the tool falls back to a direct pairwise merge and says so.

PR heads are fetched into `refs/pr-status/pull/<n>` in your clone, so later runs fetch only what changed. To remove them:

```
git for-each-ref --format='delete %(refname)' refs/pr-status/ | git update-ref --stdin
```

### Across open PRs: `-cross-pr`

The `-cross-pr` flag adds a section that fetches each PR's diff and lists every file touched by two or more PRs, with the lines each one changes and a verdict: **overlapping lines, likely conflict** or **different lines, likely merges cleanly**. Changes within 3 lines of each other count as overlapping, because Git won't auto-merge adjacent hunks. Line numbers come from each PR's own merge base, so the verdict is an estimate; use `-against` on the branch for a definitive answer.

With `-all-authors`, your PRs are compared against every open PR, not just against each other. Overlaps between two other people's PRs aren't reported.

```
Checking for cross-PR overlaps...

1 file(s) shared across PRs:

  packages/foo/nav.yml  (different lines, likely merges cleanly)
    #42     Add feature X
        L12-14
    #35     Restructure navigation
        L80, L95-101
```

### Exit code

- Status table and `-cross-pr`: exits `1` if any PR has merge conflicts, if any shared file has overlapping lines, or, with `-approved`, if any approved PR is not `READY`.
- `-files`: exits `1` if any open PR touches any of the paths.
- `-against`: exits `1` if any trial merge conflicts.
- `-files` and `-against` exit `2` on usage or setup errors, so a script can tell "overlap found" from "couldn't check".
- Otherwise exits `0`.

## Authentication

Uses your existing `gh` CLI credentials. Run `gh auth login` if you haven't already.

Alternatively, set a `GITHUB_TOKEN` environment variable.

## Credits

This tool was written by [Claude](https://claude.ai) (Anthropic).
