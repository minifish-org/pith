package tools

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	env "github.com/minifish-org/pith/packages/agent/harness/env"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	truncate "github.com/minifish-org/pith/packages/agent/harness/utils/truncate"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// The upstream tests in packages/agent/test/harness/tools.test.ts exercise the
// read tool through an in-memory NodeExecutionEnv. These Go self-tests use the
// real LocalExecutionEnv with a temporary directory and assert the same
// observable output, truncation details and image attachments.

type readTool = harnesstypes.AgentHarnessTool[ExecutionToolContext, ReadToolInput, *ReadToolDetails]

func noopReadUpdate(agenttypes.AgentToolResult[*ReadToolDetails], *harnesstypes.AgentHarnessToolUpdateOptions) {
}

func readTestEnvironment(t *testing.T) *env.LocalExecutionEnv {
	t.Helper()
	return env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: t.TempDir()})
}

func executeReadForTest(t *testing.T, tool readTool, environment *env.LocalExecutionEnv, ctx harnesscontext.Context, input ReadToolInput) (agenttypes.AgentToolResult[*ReadToolDetails], error) {
	t.Helper()
	return tool.Execute("read-test", input, noopReadUpdate, ExecutionToolContext{Env: environment}, nil, ctx)
}

func textOutputOfRead(result agenttypes.AgentToolResult[*ReadToolDetails]) string {
	var parts []string
	for _, block := range result.Content {
		if block.IsText() && block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func offsetPointer(value int) *int { return &value }

func writeReadFile(t *testing.T, environment *env.LocalExecutionEnv, name string, content []byte) {
	t.Helper()
	result := environment.WriteFile(name, content, harnesscontext.BackgroundContext)
	if !result.OK {
		t.Fatalf("write %s: %v", name, result.Error.Message)
	}
}

func TestCreateReadToolReadsText(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "file.txt", []byte("a\nb\nc\n"))
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := textOutputOfRead(result); got != "a\nb\nc\n" {
		t.Fatalf("text = %q", got)
	}
	if result.Details != nil {
		t.Fatalf("unexpected details: %#v", result.Details)
	}
}

func TestCreateReadToolOffsetAndLimitContinuation(t *testing.T) {
	environment := readTestEnvironment(t)
	lines := make([]string, 100)
	for index := range lines {
		lines[index] = "Line " + itoa(index+1)
	}
	writeReadFile(t, environment, "test.txt", []byte(strings.Join(lines, "\n")))
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "test.txt", Offset: offsetPointer(41), Limit: offsetPointer(20)})
	if err != nil {
		t.Fatal(err)
	}
	output := textOutputOfRead(result)
	if strings.Contains(output, "Line 40") || !strings.Contains(output, "Line 41") || !strings.Contains(output, "Line 60") || strings.Contains(output, "Line 61") {
		t.Fatalf("offset/limit window wrong:\n%s", output)
	}
	if !strings.Contains(output, "[40 more lines in file. Use offset=61 to continue.]") {
		t.Fatalf("missing continuation notice:\n%s", output)
	}
}

func TestCreateReadToolTruncatesByLineCount(t *testing.T) {
	environment := readTestEnvironment(t)
	lines := make([]string, 2500)
	for index := range lines {
		lines[index] = "Line " + itoa(index+1)
	}
	writeReadFile(t, environment, "large.txt", []byte(strings.Join(lines, "\n")))
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "large.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOutputOfRead(result), "[Showing lines 1-2000 of 2500. Use offset=2001 to continue.]") {
		t.Fatalf("missing truncation notice:\n%s", textOutputOfRead(result))
	}
	if result.Details == nil || result.Details.Truncation == nil {
		t.Fatal("missing truncation details")
	}
	truncation := result.Details.Truncation
	if !truncation.Truncated || truncation.TruncatedBy == nil || *truncation.TruncatedBy != "lines" || truncation.TotalLines != 2500 || truncation.OutputLines != 2000 {
		t.Fatalf("truncation = %#v", truncation)
	}
}

func TestCreateReadToolTrailingNewlineDoesNotAddLine(t *testing.T) {
	environment := readTestEnvironment(t)
	lines := make([]string, 2000)
	for index := range lines {
		lines[index] = "x"
	}
	writeReadFile(t, environment, "exact.txt", []byte(strings.Join(lines, "\n")+"\n"))
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "exact.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Details != nil {
		t.Fatalf("expected no truncation details, got %#v", result.Details)
	}
	if strings.Contains(textOutputOfRead(result), "Use offset=") {
		t.Fatal("unexpected continuation notice")
	}
}

func TestCreateReadToolRejectsOffsetBeyondEnd(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "short.txt", []byte("one\ntwo\nthree"))
	_, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "short.txt", Offset: offsetPointer(100)})
	if err == nil {
		t.Fatal("expected offset error")
	}
	if !strings.Contains(err.Error(), "Offset 100 is beyond end of file (3 lines total)") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateReadToolDetectsImageByContent(t *testing.T) {
	environment := readTestEnvironment(t)
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGD4DwABBAEAX+XDSwAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	writeReadFile(t, environment, "image.txt", png)
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "image.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOutputOfRead(result), "Read image file [image/png]") {
		t.Fatalf("text = %q", textOutputOfRead(result))
	}
	if !containsImageBlock(result.Content, base64.StdEncoding.EncodeToString(png), "image/png") {
		t.Fatalf("content = %#v", result.Content)
	}
}

func TestCreateReadToolGIFAttachment(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "image.gif", []byte("GIF89a"))
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "image.gif"})
	if err != nil {
		t.Fatal(err)
	}
	if textOutputOfRead(result) != "Read image file [image/gif]" {
		t.Fatalf("text = %q", textOutputOfRead(result))
	}
	if !containsImageBlock(result.Content, "R0lGODlh", "image/gif") {
		t.Fatalf("content = %#v", result.Content)
	}
}

func TestCreateReadToolBMPWithoutProcessorOmitsImage(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "image.bmp", createTinyBmp())
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "image.bmp"})
	if err != nil {
		t.Fatal(err)
	}
	output := textOutputOfRead(result)
	if !strings.Contains(output, "[Image omitted: configure an imageProcessor to convert BMP images.]") {
		t.Fatalf("text = %q", output)
	}
	for _, block := range result.Content {
		if block.IsImage() {
			t.Fatal("BMP image must not be attached without a processor")
		}
	}
}

func TestCreateReadToolDelegatesImageProcessor(t *testing.T) {
	environment := readTestEnvironment(t)
	bmp := createTinyBmp()
	writeReadFile(t, environment, "image.bmp", bmp)
	autoResize := false
	var receivedBytes []byte
	var receivedMime string
	var receivedAutoResize bool
	tool := CreateReadTool(&ReadToolOptions{
		AutoResizeImages: &autoResize,
		ImageProcessor: func(bytes []byte, mimeType string, options ReadImageProcessorOptions, _ harnesscontext.Context) (ReadImageProcessorResult, error) {
			receivedBytes = bytes
			receivedMime = mimeType
			receivedAutoResize = options.AutoResizeImages
			return ReadImageProcessorResult{OK: true, Data: "converted", MimeType: "image/png", Hints: []string{"[Image converted from image/bmp to image/png.]"}}, nil
		},
	})

	result, err := executeReadForTest(t, tool, environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "image.bmp"})
	if err != nil {
		t.Fatal(err)
	}
	if receivedMime != "image/bmp" || receivedAutoResize {
		t.Fatalf("processor options = %q autoResize=%v", receivedMime, receivedAutoResize)
	}
	if string(receivedBytes) != string(bmp) {
		t.Fatal("processor did not receive the file bytes")
	}
	output := textOutputOfRead(result)
	if !strings.Contains(output, "[Image converted from image/bmp to image/png.]") {
		t.Fatalf("text = %q", output)
	}
	if !containsImageBlock(result.Content, "converted", "image/png") {
		t.Fatalf("content = %#v", result.Content)
	}
}

func TestCreateReadToolImageProcessorFailure(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "image.bmp", createTinyBmp())
	tool := CreateReadTool(&ReadToolOptions{
		ImageProcessor: func([]byte, string, ReadImageProcessorOptions, harnesscontext.Context) (ReadImageProcessorResult, error) {
			return ReadImageProcessorResult{OK: false, Message: "cannot convert"}, nil
		},
	})

	result, err := executeReadForTest(t, tool, environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "image.bmp"})
	if err != nil {
		t.Fatal(err)
	}
	output := textOutputOfRead(result)
	if !strings.Contains(output, "Read image file [image/bmp]\ncannot convert") {
		t.Fatalf("text = %q", output)
	}
}

func TestCreateReadToolCRLFAndUTF8(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "file.txt", []byte("中\r\n文"))
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := textOutputOfRead(result); got != "中\r\n文" {
		t.Fatalf("text = %q", got)
	}
}

func TestCreateReadToolFirstLineExceedsLimit(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "big.txt", []byte(strings.Repeat("x", truncate.DefaultMaxBytes+1)))
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	output := textOutputOfRead(result)
	if !strings.Contains(output, "exceeds 50.0KB limit. Use bash: sed -n '1p' big.txt | head -c 51200]") {
		t.Fatalf("text = %q", output)
	}
	if result.Details == nil || result.Details.Truncation == nil || !result.Details.Truncation.FirstLineExceedsLimit {
		t.Fatalf("details = %#v", result.Details)
	}
}

func TestCreateReadToolUnsupportedImageIsText(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "file.txt", []byte{0xff, 0xd8, 0xff, 0xf7, 'h', 'i'})
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range result.Content {
		if block.IsImage() {
			t.Fatal("unsupported image bytes must be treated as text")
		}
	}
	if !strings.Contains(textOutputOfRead(result), "hi") {
		t.Fatalf("text = %q", textOutputOfRead(result))
	}
}

func TestCreateReadToolCancellation(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "file.txt", []byte("hello"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := executeReadForTest(t, CreateReadTool(nil), environment, ctx, ReadToolInput{Path: "file.txt"})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestCreateReadToolPrepareArguments(t *testing.T) {
	tool := CreateReadTool(nil)
	params, err := tool.PrepareArguments(json.RawMessage(`{"path":"a.txt","offset":2,"limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if params.Path != "a.txt" || params.Offset == nil || *params.Offset != 2 || params.Limit == nil || *params.Limit != 3 {
		t.Fatalf("params = %#v", params)
	}
}

func TestCreateReadToolResolvesAtPrefix(t *testing.T) {
	environment := readTestEnvironment(t)
	writeReadFile(t, environment, "plain.txt", []byte("hello"))
	result, err := executeReadForTest(t, CreateReadTool(nil), environment, harnesscontext.BackgroundContext, ReadToolInput{Path: "@plain.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if textOutputOfRead(result) != "hello" {
		t.Fatalf("text = %q", textOutputOfRead(result))
	}
}

func containsImageBlock(blocks []aitypes.ContentBlock, data string, mimeType string) bool {
	for _, block := range blocks {
		if block.IsImage() && block.Image != nil && block.Image.Data == data && block.Image.MimeType == mimeType {
			return true
		}
	}
	return false
}

func createTinyBmp() []byte {
	bytes := make([]byte, 58)
	binary.LittleEndian.PutUint16(bytes[0:], 0x4d42)
	binary.LittleEndian.PutUint32(bytes[2:], uint32(len(bytes)))
	binary.LittleEndian.PutUint32(bytes[10:], 54)
	binary.LittleEndian.PutUint32(bytes[14:], 40)
	binary.LittleEndian.PutUint32(bytes[18:], 1)
	binary.LittleEndian.PutUint32(bytes[22:], 1)
	binary.LittleEndian.PutUint16(bytes[26:], 1)
	binary.LittleEndian.PutUint16(bytes[28:], 24)
	binary.LittleEndian.PutUint32(bytes[34:], 4)
	return bytes
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
