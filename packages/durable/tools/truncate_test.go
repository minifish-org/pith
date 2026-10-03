package tools

import "testing"

func TestTruncateReportsUTF8ByteCounts(t *testing.T) {
	content := "aé🙂\nb"
	result := TruncateHead(content, TruncationOptions{MaxBytes: intPointer(100), MaxLines: intPointer(10)})
	if result.Truncated {
		t.Fatal("unexpected truncation")
	}
	if result.TotalBytes != 9 || result.OutputBytes != 9 {
		t.Fatalf("bytes = %d/%d, want 9/9", result.TotalBytes, result.OutputBytes)
	}
}

func TestTruncateDoesNotCountTrailingNewline(t *testing.T) {
	content := "line\nline\nline\n"
	head := TruncateHead(content, TruncationOptions{MaxBytes: intPointer(100), MaxLines: intPointer(3)})
	if head.Truncated || head.TotalLines != 3 || head.OutputLines != 3 {
		t.Fatalf("head = %#v", head)
	}
}

func TestTruncateHeadByLineLimits(t *testing.T) {
	head := TruncateHead("one\ntwo\nthree\nfour", TruncationOptions{MaxBytes: intPointer(100), MaxLines: intPointer(2)})
	if head.Content != "one\ntwo" || !head.Truncated || head.TruncatedBy == nil || *head.TruncatedBy != "lines" || head.TotalLines != 4 || head.OutputLines != 2 {
		t.Fatalf("head = %#v", head)
	}
}

func TestTruncateReportsBytesWhenTrailingNewlineExceedsAtLineCap(t *testing.T) {
	head := TruncateHead("hello\nworld\n", TruncationOptions{MaxBytes: intPointer(11), MaxLines: intPointer(2)})
	if head.Content != "hello\nworld" || !head.Truncated || head.TruncatedBy == nil || *head.TruncatedBy != "bytes" || head.TotalLines != 2 || head.OutputLines != 2 {
		t.Fatalf("head = %#v", head)
	}
}

func TestTruncateHeadUTF8ByteLimit(t *testing.T) {
	result := TruncateHead("éé\nabc", TruncationOptions{MaxBytes: intPointer(4), MaxLines: intPointer(10)})
	if result.Content != "éé" || !result.Truncated || result.TruncatedBy == nil || *result.TruncatedBy != "bytes" || result.OutputBytes != 4 || result.FirstLineExceedsLimit {
		t.Fatalf("result = %#v", result)
	}
}

func TestTruncateHeadFirstLineExceedsByteLimit(t *testing.T) {
	result := TruncateHead("éé\nabc", TruncationOptions{MaxBytes: intPointer(3), MaxLines: intPointer(10)})
	if result.Content != "" || !result.Truncated || result.TruncatedBy == nil || *result.TruncatedBy != "bytes" || !result.FirstLineExceedsLimit {
		t.Fatalf("result = %#v", result)
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int]string{
		1023:            "1023B",
		1536:            "1.5KB",
		3 * 1024 * 1024: "3.0MB",
	}
	for bytes, want := range cases {
		if got := FormatSize(bytes); got != want {
			t.Fatalf("FormatSize(%d) = %q, want %q", bytes, got, want)
		}
	}
}

func intPointer(value int) *int { return &value }
