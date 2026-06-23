# pr-conflicts

A command-line tool that checks your open GitHub pull requests for merge conflicts.

GitHub computes mergeability lazily and asynchronously. This tool fetches each PR individually (which triggers that computation) and retries any that are still pending, so you get a definitive answer rather than a list of unknowns.

## Installation

```
go install github.com/enlarsen/pr-conflicts@latest
```

Make sure `$(go env GOPATH)/bin` is in your `PATH`.

## Usage

Run from inside any GitHub repository to check your own open PRs:

```
pr-conflicts
```

Or specify a repository explicitly:

```
pr-conflicts -repo owner/repo
```

### Flags

| Flag | Description |
|------|-------------|
| `-repo owner/repo` | Repository to check (auto-detected from `git remote` if omitted) |
| `-author username` | GitHub user whose PRs to check (defaults to the authenticated user) |
| `-conflicts-only` | Only show PRs with merge conflicts |
| `-cross-pr` | Also report files touched by more than one open PR |

### Cross-PR conflict detection

The `--cross-pr` flag adds a second section that fetches the changed-file list for each PR and identifies any file touched by two or more PRs. These are potential conflicts that GitHub's mergeability check won't catch — whichever of the overlapping PRs merges second will need a rebase.

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

Exits `1` if any conflicts are found, `0` otherwise — useful in scripts.

## Authentication

Uses your existing `gh` CLI credentials. Run `gh auth login` if you haven't already.

Alternatively, set a `GITHUB_TOKEN` environment variable.

## Credits

This tool was written by [Claude](https://claude.ai) (Anthropic).
