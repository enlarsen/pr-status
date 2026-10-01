# pr-status

A command-line tool that tells you whether your open GitHub pull requests can be merged: merge conflicts, review decision, branch-protection state, and CI check results in one table. (Formerly `pr-conflicts`, which only reported conflicts.)

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
| `-cross-pr` | Also report files touched by more than one open PR |

### Cross-PR conflict detection

The `-cross-pr` flag adds a second section that fetches the changed-file list for each PR and identifies any file touched by two or more PRs. These are potential conflicts that GitHub's mergeability check won't catch: whichever of the overlapping PRs merges second will need a rebase.

```
Checking for cross-PR file overlaps...

2 file(s) shared across PRs:

  packages/foo/bar.md
    #42   Add feature X
    #38   Update feature Y

  packages/foo/nav.yml
    #42   Add feature X
    #35   Restructure navigation
```

### Exit code

Exits `1` if any conflicts or cross-PR overlaps are found, or, with `-approved`, if any approved PR is not `READY`. Exits `0` otherwise, which is useful in scripts.

## Authentication

Uses your existing `gh` CLI credentials. Run `gh auth login` if you haven't already.

Alternatively, set a `GITHUB_TOKEN` environment variable.

## Credits

This tool was written by [Claude](https://claude.ai) (Anthropic).
