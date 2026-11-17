package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// lineRange is an inclusive range of line numbers in the base version of a file.
// A pure insertion (no base lines removed) is recorded as a zero-width range at the
// line it follows, with Start == End.
type lineRange struct {
	Start, End int
}

func (r lineRange) String() string {
	if r.Start == r.End {
		return fmt.Sprintf("L%d", r.Start)
	}
	return fmt.Sprintf("L%d-%d", r.Start, r.End)
}

// hunkChange is what one PR does to one file: the base line ranges it touches plus
// any headings it adds or removes outright.
type hunkChange struct {
	Ranges          []lineRange
	AddedHeadings   []string
	RemovedHeadings []string
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+\d+(?:,\d+)? @@`)
var atxHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

// parsePatch reads a unified diff for a single file (the `patch` field of GitHub's
// pulls/N/files response) and returns the base ranges each hunk touches. Ranges are
// narrowed to the lines actually removed or changed, not the surrounding context, so
// two PRs that only share context lines are not reported as overlapping. A hunk that
// only adds lines is recorded at the base line the addition follows.
func parsePatch(patch string) hunkChange {
	var hc hunkChange
	baseLine := 0
	inHunk := false
	runStart := -1 // first base line of the current run of changes, -1 when none

	flush := func(end int) {
		if runStart >= 0 {
			hc.Ranges = append(hc.Ranges, lineRange{runStart, end})
			runStart = -1
		}
	}

	for _, line := range strings.Split(patch, "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			flush(baseLine - 1)
			baseLine, _ = strconv.Atoi(m[1])
			// "-0,0" means the file is new; "-N,0" means insertion after line N, in
			// which case the first base line of the hunk is N+1.
			if m[2] == "0" {
				baseLine++
			}
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case ' ':
			flush(baseLine - 1)
			baseLine++
		case '-':
			if runStart < 0 {
				runStart = baseLine
			}
			if h := headingText(line[1:]); h != "" {
				hc.RemovedHeadings = append(hc.RemovedHeadings, h)
			}
			baseLine++
		case '+':
			if runStart < 0 {
				// Insertion between base lines: anchor it to the preceding line.
				anchor := baseLine - 1
				if anchor < 1 {
					anchor = 1
				}
				hc.Ranges = append(hc.Ranges, lineRange{anchor, anchor})
			}
			if h := headingText(line[1:]); h != "" {
				hc.AddedHeadings = append(hc.AddedHeadings, h)
			}
		case '\\': // "\ No newline at end of file"
		}
	}
	flush(baseLine - 1)
	hc.Ranges = mergeRanges(hc.Ranges, 0)
	// A heading both removed and re-added (a reworded section body around an unchanged
	// title, or a whitespace tweak) is neither new nor gone.
	hc.AddedHeadings, hc.RemovedHeadings = cancelPairs(hc.AddedHeadings, hc.RemovedHeadings)
	return hc
}

func headingText(line string) string {
	m := atxHeading.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	// Drop an explicit {#id} suffix so the name reads like the rendered heading.
	text := m[2]
	if i := strings.Index(text, " {#"); i >= 0 && strings.HasSuffix(text, "}") {
		text = strings.TrimSpace(text[:i])
	}
	return text
}

func cancelPairs(added, removed []string) ([]string, []string) {
	count := map[string]int{}
	for _, r := range removed {
		count[r]++
	}
	var keptAdded []string
	for _, a := range added {
		if count[a] > 0 {
			count[a]--
			continue
		}
		keptAdded = append(keptAdded, a)
	}
	var keptRemoved []string
	for _, r := range removed {
		if count[r] > 0 {
			count[r]--
			keptRemoved = append(keptRemoved, r)
		}
	}
	return keptAdded, keptRemoved
}

// mergeRanges sorts ranges and joins any that overlap or sit within slack lines of
// each other.
func mergeRanges(rs []lineRange, slack int) []lineRange {
	if len(rs) == 0 {
		return nil
	}
	sorted := append([]lineRange(nil), rs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	out := []lineRange{sorted[0]}
	for _, r := range sorted[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End+slack+1 {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// rangesOverlap reports whether any range in a comes within slack lines of any range
// in b. Git refuses to auto-merge hunks that touch or abut, so a little slack makes
// the estimate match what a rebase would actually hit. The two PRs may have branched
// from different commits, so line numbers can drift; treat the answer as an estimate
// and use -against for a definitive one.
func rangesOverlap(a, b []lineRange, slack int) bool {
	for _, x := range a {
		for _, y := range b {
			if x.Start <= y.End+slack && y.Start <= x.End+slack {
				return true
			}
		}
	}
	return false
}

// heading is one ATX heading in a markdown file.
type heading struct {
	Line  int
	Level int
	Text  string
}

// parseHeadings lists the ATX headings in a markdown file, skipping fenced code blocks
// and the YAML frontmatter.
func parseHeadings(content string) []heading {
	var hs []heading
	inFence := false
	fence := ""
	lines := strings.Split(content, "\n")
	start := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				start = i + 1
				break
			}
		}
	}
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimLeft(lines[i], " ")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if !inFence {
				inFence, fence = true, marker
			} else if marker == fence {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		if m := atxHeading.FindStringSubmatch(lines[i]); m != nil {
			hs = append(hs, heading{Line: i + 1, Level: len(m[1]), Text: headingText(lines[i])})
		}
	}
	return hs
}

// sectionsFor names the headings whose sections the ranges touch: the heading each
// range starts under, plus any heading that falls inside a range. Lines above the
// first heading are reported as "(top of page)".
func sectionsFor(ranges []lineRange, hs []heading) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, r := range ranges {
		enclosing := "(top of page)"
		for _, h := range hs {
			if h.Line <= r.Start {
				enclosing = h.Text
			} else {
				break
			}
		}
		add(enclosing)
		for _, h := range hs {
			if h.Line > r.Start && h.Line <= r.End {
				add(h.Text)
			}
		}
	}
	return out
}

// describeChange renders a hunkChange as section names when headings are known, or
// as line ranges otherwise.
func describeChange(hc hunkChange, hs []heading) string {
	var parts []string
	if hs != nil {
		for _, s := range sectionsFor(hc.Ranges, hs) {
			parts = append(parts, "§"+s)
		}
	} else {
		for _, r := range hc.Ranges {
			parts = append(parts, r.String())
		}
	}
	for _, h := range hc.AddedHeadings {
		parts = append(parts, "+§"+h)
	}
	for _, h := range hc.RemovedHeadings {
		replaced := false
		for i, p := range parts {
			if p == "§"+h {
				parts[i], replaced = "-§"+h, true
				break
			}
		}
		if !replaced {
			parts = append(parts, "-§"+h)
		}
	}
	if len(parts) == 0 {
		return "(no line changes: rename or mode change)"
	}
	return strings.Join(parts, ", ")
}
