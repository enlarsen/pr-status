package main

import (
	"reflect"
	"testing"
)

func TestParsePatchModification(t *testing.T) {
	patch := `@@ -10,7 +10,8 @@ context
 line10
 line11
-line12
-line13
+new12
+new13
+new14
 line14
 line15
 line16`
	hc := parsePatch(patch)
	want := []lineRange{{12, 13}}
	if !reflect.DeepEqual(hc.Ranges, want) {
		t.Fatalf("ranges = %v, want %v", hc.Ranges, want)
	}
}

func TestParsePatchPureInsertion(t *testing.T) {
	// Three lines inserted after base line 20.
	patch := `@@ -19,2 +19,5 @@
 line19
 line20
+a
+b
+c`
	hc := parsePatch(patch)
	want := []lineRange{{20, 20}}
	if !reflect.DeepEqual(hc.Ranges, want) {
		t.Fatalf("ranges = %v, want %v", hc.Ranges, want)
	}
}

func TestParsePatchInsertionWithoutContext(t *testing.T) {
	// "-5,0" means insert after line 5.
	hc := parsePatch("@@ -5,0 +6,2 @@\n+a\n+b")
	want := []lineRange{{5, 5}}
	if !reflect.DeepEqual(hc.Ranges, want) {
		t.Fatalf("ranges = %v, want %v", hc.Ranges, want)
	}
}

func TestParsePatchMultipleHunksAndHeadings(t *testing.T) {
	patch := `@@ -1,3 +1,3 @@
 ---
-title: Old
+title: New
 ---
@@ -40,4 +40,7 @@
 text
-## Rules List
+## Rule View
+
+### Rule Page
 more
 more
 more`
	hc := parsePatch(patch)
	want := []lineRange{{2, 2}, {41, 41}}
	if !reflect.DeepEqual(hc.Ranges, want) {
		t.Fatalf("ranges = %v, want %v", hc.Ranges, want)
	}
	if !reflect.DeepEqual(hc.AddedHeadings, []string{"Rule View", "Rule Page"}) {
		t.Errorf("added = %v", hc.AddedHeadings)
	}
	if !reflect.DeepEqual(hc.RemovedHeadings, []string{"Rules List"}) {
		t.Errorf("removed = %v", hc.RemovedHeadings)
	}
}

func TestParsePatchReaddedHeadingCancels(t *testing.T) {
	hc := parsePatch("@@ -3,1 +3,1 @@\n-## Same {#same}\n+## Same")
	if len(hc.AddedHeadings) != 0 || len(hc.RemovedHeadings) != 0 {
		t.Fatalf("added=%v removed=%v, want both empty", hc.AddedHeadings, hc.RemovedHeadings)
	}
}

func TestRangesOverlap(t *testing.T) {
	a := []lineRange{{10, 12}}
	cases := []struct {
		b     []lineRange
		slack int
		want  bool
	}{
		{[]lineRange{{12, 14}}, 0, true},
		{[]lineRange{{14, 20}}, 0, false},
		{[]lineRange{{14, 20}}, 2, true},
		{[]lineRange{{1, 5}, {30, 31}}, 2, false},
	}
	for _, c := range cases {
		if got := rangesOverlap(a, c.b, c.slack); got != c.want {
			t.Errorf("rangesOverlap(%v, %v, %d) = %v, want %v", a, c.b, c.slack, got, c.want)
		}
	}
}

const sampleDoc = `---
title: Understand results
id: dh-understand-results
---

Intro text.

## Learn More

Para.

` + "```md\n## Not a heading\n```" + `

### Rules List

Rules.

## Filtering and Exporting {#filtering-and-exporting}

Filters.
`

func TestParseHeadings(t *testing.T) {
	hs := parseHeadings(sampleDoc)
	var texts []string
	for _, h := range hs {
		texts = append(texts, h.Text)
	}
	want := []string{"Learn More", "Rules List", "Filtering and Exporting"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("headings = %v, want %v", texts, want)
	}
	if hs[0].Line != 8 {
		t.Errorf("Learn More on line %d, want 8", hs[0].Line)
	}
}

func TestSectionsFor(t *testing.T) {
	hs := parseHeadings(sampleDoc)
	// Line 6 is the intro, 18 is under Rules List, and 18-22 crosses into Filtering.
	got := sectionsFor([]lineRange{{6, 6}, {18, 22}}, hs)
	want := []string{"(top of page)", "Rules List", "Filtering and Exporting"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sections = %v, want %v", got, want)
	}
}

func TestDescribeChangeMarksRemovedSection(t *testing.T) {
	hs := []heading{{Line: 10, Level: 2, Text: "Rules List"}}
	hc := hunkChange{Ranges: []lineRange{{10, 12}}, RemovedHeadings: []string{"Rules List"}, AddedHeadings: []string{"Rule View"}}
	if got := describeChange(hc, hs); got != "-§Rules List, +§Rule View" {
		t.Fatalf("got %q", got)
	}
}
