package truncate

import (
	"encoding/json"
	"testing"
)

func TestTruncateHeadBehavior(t *testing.T) {
	tests := []struct {
		name    string
		content string
		options TruncationOptions
		want    string
		by      string
		partial bool
		first   bool
		lines   int
		out     int
		outB    int
	}{
		{name: "line limit", content: "a\nb\nc", options: TruncationOptions{MaxLines: intPtr(2), MaxBytes: intPtr(100)}, want: "a\nb", by: "lines", lines: 3, out: 2, outB: 3},
		{name: "byte limit complete line", content: "你好\n世界", options: TruncationOptions{MaxLines: intPtr(5), MaxBytes: intPtr(7)}, want: "你好", by: "bytes", lines: 2, out: 1, outB: 6},
		{name: "first line exceeds", content: "abcdef", options: TruncationOptions{MaxLines: intPtr(5), MaxBytes: intPtr(3)}, want: "", by: "bytes", first: true, lines: 1, out: 0, outB: 0},
		{name: "no truncation", content: "", options: TruncationOptions{MaxLines: intPtr(2), MaxBytes: intPtr(10)}, want: "", by: "", lines: 0, out: 0, outB: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := TruncateHead(test.content, test.options)
			if result.Content != test.want {
				t.Fatalf("content = %q, want %q", result.Content, test.want)
			}
			if test.by == "" {
				if result.TruncatedBy != nil || result.Truncated {
					t.Fatalf("expected untruncated, got %+v", result)
				}
			} else {
				if result.TruncatedBy == nil || *result.TruncatedBy != test.by {
					t.Fatalf("truncatedBy = %v, want %q", result.TruncatedBy, test.by)
				}
			}
			if result.LastLinePartial != test.partial {
				t.Fatalf("lastLinePartial = %v, want %v", result.LastLinePartial, test.partial)
			}
			if result.FirstLineExceedsLimit != test.first {
				t.Fatalf("firstLineExceedsLimit = %v, want %v", result.FirstLineExceedsLimit, test.first)
			}
			if result.TotalLines != test.lines || result.OutputLines != test.out || result.OutputBytes != test.outB {
				t.Fatalf("counts = %+v", result)
			}
		})
	}
}

func TestTruncateTailBehavior(t *testing.T) {
	tests := []struct {
		name    string
		content string
		options TruncationOptions
		want    string
		by      string
		partial bool
	}{
		{name: "line limit", content: "a\nb\nc", options: TruncationOptions{MaxLines: intPtr(2), MaxBytes: intPtr(100)}, want: "b\nc", by: "lines"},
		{name: "partial line", content: "你好世界", options: TruncationOptions{MaxLines: intPtr(5), MaxBytes: intPtr(7)}, want: "世界", by: "bytes", partial: true},
		{name: "crlf no truncation", content: "a\r\nb\r\n", options: TruncationOptions{MaxLines: intPtr(2), MaxBytes: intPtr(100)}, want: "a\r\nb\r\n", by: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := TruncateTail(test.content, test.options)
			if result.Content != test.want {
				t.Fatalf("content = %q, want %q", result.Content, test.want)
			}
			if test.by == "" {
				if result.Truncated {
					t.Fatalf("expected untruncated: %+v", result)
				}
			} else if result.TruncatedBy == nil || *result.TruncatedBy != test.by {
				t.Fatalf("truncatedBy = %v, want %q", result.TruncatedBy, test.by)
			}
			if result.LastLinePartial != test.partial {
				t.Fatalf("lastLinePartial = %v, want %v", result.LastLinePartial, test.partial)
			}
		})
	}
}

func TestTruncationResultJSONRoundTrip(t *testing.T) {
	result := TruncateHead("a\nb\nc", TruncationOptions{MaxLines: intPtr(2), MaxBytes: intPtr(100)})
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["truncatedBy"] != "lines" {
		t.Fatalf("truncatedBy = %v", decoded["truncatedBy"])
	}
	if decoded["content"] != "a\nb" {
		t.Fatalf("content = %v", decoded["content"])
	}
	untruncated := TruncateHead("a", TruncationOptions{})
	encoded, err = json.Marshal(untruncated)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["truncatedBy"] != nil {
		t.Fatalf("expected null truncatedBy, got %v", decoded["truncatedBy"])
	}
}

func TestUtf8ByteLengthAndFormatSize(t *testing.T) {
	if got := Utf8ByteLength("你好\n世界"); got != 13 {
		t.Fatalf("Utf8ByteLength = %d, want 13", got)
	}
	if got := FormatSize(512); got != "512B" {
		t.Fatalf("FormatSize = %q", got)
	}
	if got := FormatSize(2048); got != "2.0KB" {
		t.Fatalf("FormatSize = %q", got)
	}
}

func TestTruncateLine(t *testing.T) {
	text, truncated := TruncateLine("abcdef", 3)
	if !truncated || text != "abc... [truncated]" {
		t.Fatalf("got %q truncated=%v", text, truncated)
	}
	text, truncated = TruncateLine("ab", 3)
	if truncated || text != "ab" {
		t.Fatalf("got %q truncated=%v", text, truncated)
	}
}

func intPtr(value int) *int { return &value }
