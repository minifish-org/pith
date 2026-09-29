package tools

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	env "github.com/minifish-org/pith/packages/agent/harness/env"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

func TestDetectSupportedImageMimeType(t *testing.T) {
	cases := []struct {
		name   string
		buffer []byte
		want   string
	}{
		{"empty", nil, ""},
		{"unknown", []byte{0, 1, 2}, ""},
		{"gif87a", []byte("GIF87a...."), "image/gif"},
		{"gif89a", []byte("GIF89a...."), "image/gif"},
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0}, "image/jpeg"},
		{"jpeg-extension-marker", []byte{0xff, 0xd8, 0xff, 0xf7}, ""},
		{"webp", append(append([]byte("RIFF"), make([]byte, 4)...), []byte("WEBP...")...), "image/webp"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := DetectSupportedImageMimeType(testCase.buffer)
			if testCase.want == "" {
				if got != nil {
					t.Fatalf("expected nil, got %q", *got)
				}
				return
			}
			if got == nil || *got != testCase.want {
				t.Fatalf("got %v, want %q", got, testCase.want)
			}
		})
	}
}

func pngHeader() []byte {
	buffer := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	buffer = append(buffer, 0, 0, 0, 13)
	buffer = append(buffer, []byte("IHDR")...)
	buffer = append(buffer, make([]byte, 13)...)
	buffer = append(buffer, make([]byte, 4)...) // IHDR CRC
	return buffer
}

func TestDetectSupportedImageMimeTypePng(t *testing.T) {
	if got := DetectSupportedImageMimeType(pngHeader()); got == nil || *got != "image/png" {
		t.Fatalf("plain PNG = %v", got)
	}
	animated := append(pngHeader(), 0, 0, 0, 0)
	animated = append(animated, []byte("acTL")...)
	animated = append(animated, make([]byte, 4)...)
	if got := DetectSupportedImageMimeType(animated); got != nil {
		t.Fatalf("animated PNG should be rejected, got %q", *got)
	}
}

func TestEncodeBase64(t *testing.T) {
	cases := map[string]string{
		"":      "",
		"f":     "Zg==",
		"fo":    "Zm8=",
		"foo":   "Zm9v",
		"hello": "aGVsbG8=",
	}
	for input, expected := range cases {
		if got := EncodeBase64([]byte(input)); got != expected {
			t.Fatalf("EncodeBase64(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestResolveToolPathAndReadVariants(t *testing.T) {
	root := t.TempDir()
	environment := env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()
	harnesstypes.GetOrThrow(environment.WriteFile("plain.txt", []byte("hello"), ctx))

	resolved := ResolveToolPath(environment, "@plain.txt", ctx)
	if resolved != filepath.Join(root, "plain.txt") {
		t.Fatalf("ResolveToolPath = %q", resolved)
	}
	if got := ResolveReadToolPath(environment, "plain.txt", ctx); got != filepath.Join(root, "plain.txt") {
		t.Fatalf("ResolveReadToolPath = %q", got)
	}

	// A typographic apostrophe file should be found from its ASCII spelling.
	typographic := filepath.Join(root, "it\u2019s.txt")
	if err := os.WriteFile(typographic, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveReadToolPath(environment, "it's.txt", ctx); got != typographic {
		t.Fatalf("apostrophe variant = %q, want %q", got, typographic)
	}
}

func TestWithFileMutationQueueSerializesSamePath(t *testing.T) {
	root := t.TempDir()
	environment := env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()
	harnesstypes.GetOrThrow(environment.WriteFile("file.txt", []byte("x"), ctx))

	var mu sync.Mutex
	active := 0
	maxActive := 0
	var waitGroup sync.WaitGroup
	for index := 0; index < 8; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, _ = WithFileMutationQueue(environment, "file.txt", func() (int, error) {
				mu.Lock()
				active++
				if active > maxActive {
					maxActive = active
				}
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				mu.Lock()
				active--
				mu.Unlock()
				return 0, nil
			}, harnesscontext.BackgroundContext)
		}()
	}
	waitGroup.Wait()
	if maxActive != 1 {
		t.Fatalf("mutations overlapped: maxActive = %d", maxActive)
	}
}
