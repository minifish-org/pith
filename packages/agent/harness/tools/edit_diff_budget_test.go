package tools

import (
	"strings"
	"testing"
)

func TestBoundedPatchMatchesNormalPatchForSmallEdits(t *testing.T) {
	for _, contents := range [][2]string{{"", ""}, {"a\nb\nc\n", "a\nB\nc\n"}, {"one", "two"}, {"", "created\n"}} {
		want := GenerateUnifiedPatch("note.txt", contents[0], contents[1])
		if got := GenerateUnifiedPatchBounded("note.txt", contents[0], contents[1], 100_000); got != want {
			t.Fatalf("bounded patch differs: %q / %q", got, want)
		}
		wantDiff, wantLine := GenerateDiffString(contents[0], contents[1])
		gotDiff, gotLine := GenerateDiffStringBounded(contents[0], contents[1], 100_000)
		if gotDiff != wantDiff || (gotLine == nil) != (wantLine == nil) || (gotLine != nil && *gotLine != *wantLine) {
			t.Fatal("bounded display diff changed ordinary edit behavior")
		}
	}
}

func TestBoundedPatchFallsBackWithoutLosingChanges(t *testing.T) {
	before := "prefix\n" + strings.Repeat("old\n", 20_000) + "suffix\n"
	after := "prefix\n" + strings.Repeat("new\n", 20_000) + "suffix\n"
	patch := GenerateUnifiedPatchBounded("large.txt", before, after, 100_000)
	if strings.Count(patch, "\n-old") != 20_000 || strings.Count(patch, "\n+new") != 20_000 || !strings.Contains(patch, " prefix\n") || !strings.Contains(patch, " suffix\n") {
		t.Fatal("fallback patch lost data or context")
	}
	diff, line := GenerateDiffStringBounded(before, after, 100_000)
	if line == nil || *line != 2 || strings.Count(diff, " old") != 20_000 || strings.Count(diff, " new") != 20_000 {
		t.Fatal("fallback display diff lost data or changed line")
	}
}
