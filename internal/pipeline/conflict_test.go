package pipeline

import (
	"bytes"
	"testing"
)

func TestParseConflicts(t *testing.T) {
	t.Parallel()
	content := []byte("line 1\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> branch\nline 2\n")
	regions := ParseConflicts("foo.txt", content)
	if len(regions) != 1 {
		t.Fatalf("expected 1 conflict region, got %d", len(regions))
	}
	r := regions[0]
	if r.StartLine != 2 || r.EndLine != 6 {
		t.Fatalf("unexpected conflict bounds: %d-%d", r.StartLine, r.EndLine)
	}
	if r.Ours != "ours" || r.Theirs != "theirs" {
		t.Fatalf("unexpected conflict payload: %#v", r)
	}
}

func TestParseConflictsSupportsMultipleRegions(t *testing.T) {
	t.Parallel()
	content := []byte("<<<<<<< HEAD\n1\n=======\n2\n>>>>>>> branch\nmid\n<<<<<<< HEAD\nA\n=======\nB\n>>>>>>> branch\n")
	regions := ParseConflicts("bar.txt", content)
	if len(regions) != 2 {
		t.Fatalf("expected 2 conflict regions, got %d", len(regions))
	}
	if !bytes.Equal([]byte(regions[0].Ours), []byte("1")) {
		t.Fatalf("unexpected first region ours: %q", regions[0].Ours)
	}
	if !bytes.Equal([]byte(regions[1].Ours), []byte("A")) {
		t.Fatalf("unexpected second region ours: %q", regions[1].Ours)
	}
}

func TestParseConflictsSupportsDiff3Style(t *testing.T) {
	t.Parallel()
	content := []byte("<<<<<<< HEAD\nours\n||||||| base\na\n=======\ntheirs\n>>>>>>> branch\n")
	regions := ParseConflicts("diff3.txt", content)
	if len(regions) != 1 {
		t.Fatalf("expected 1 conflict region, got %d", len(regions))
	}
	region := regions[0]
	if region.StartLine != 1 || region.EndLine != 7 {
		t.Fatalf("unexpected conflict bounds: %d-%d", region.StartLine, region.EndLine)
	}
	if region.Ours != "ours" || region.Base != "a" || region.Theirs != "theirs" {
		t.Fatalf("unexpected diff3 payload: %#v", region)
	}
}

func TestHasConflictMarkers(t *testing.T) {
	t.Parallel()
	if !HasConflictMarkers([]byte("<<<<<<<<< HEAD")) {
		t.Fatal("expected marker detection")
	}
	if HasConflictMarkers([]byte("plain text")) {
		t.Fatal("did not expect marker detection")
	}
}

func TestCountConflictLines(t *testing.T) {
	t.Parallel()
	regions := []ConflictRegion{
		{StartLine: 2, EndLine: 6},
		{StartLine: 12, EndLine: 14},
	}
	if got := CountConflictLines(regions); got != 8 {
		t.Fatalf("expected 8 lines, got %d", got)
	}
}
