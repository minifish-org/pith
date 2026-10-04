# Images and prompt inputs

The headless behaviors in Pi `agent-session.ts` at
`a13d35a742c6ef8462812a28fbe1d8c8b7431c32` use one set of Go entry points:
`Prompt(ctx, text, options...)`, `Steer(text, options...)` and
`FollowUp(text, options...)`. Supply zero or one `PromptOptions` value. All
inputs use the same behavior: loaded skills/templates expand by default. There
are no separate legacy or `WithOptions` methods.

## Attach an image

```go
data, err := os.ReadFile("screenshot.png")
if err != nil {
    return err
}
images := []aitypes.ImageContent{
    aitypes.NewImageContent(base64.StdEncoding.EncodeToString(data), "image/png"),
}
result, err := session.Prompt(ctx, "Describe this screenshot", codingagent.PromptOptions{
    Images: images,
})
```

This snippet uses `os`, `encoding/base64`,
`github.com/minifish-org/pith/packages/ai/types` as `aitypes`, and
`github.com/minifish-org/pith/packages/coding-agent` as `codingagent`. It assumes
an already-created session using a model/provider that accepts image input.
Text-only model names do not acquire vision by attaching an image.

Images remain structured message content in the agent loop, provider request
and JSONL transcript. A retry continues the existing context instead of
appending the same input again. Caller image slices are copied at submission;
returned transcript/result snapshots do not share image blocks with the session.

Pith does not currently resize, transcode or validate image bytes against a
model's image limits. The supplied MIME type and base64 bytes pass through the
existing provider adapter. Hosts should prepare images their provider accepts.
This patch adds no image size, token or turn cap and no CGO dependency.

## Templates and skills

```go
// Expand a template previously selected through ResourceOptions.TemplatePaths.
_, err := session.Prompt(ctx, `/review "changed files"`, codingagent.PromptOptions{})

// Expand a previously loaded skill, including its location and reference base.
_, err = session.Prompt(ctx, "/skill:review check the diff", codingagent.PromptOptions{})

// Send slash-prefixed text literally.
expand := false
_, err = session.Prompt(ctx, "/review literal text", codingagent.PromptOptions{
    ExpandPromptTemplates: &expand,
})
```

All three methods expand by default, including calls without options. Unknown commands remain literal.
Only selected, loaded resources are used; commands and JS extensions are not
executed. Skills are re-read at submission, as in Pi. A failed skill read or
frontmatter parse returns an error before a model call; the Go API has no TS
extension error channel to report it through.

## Queue another input

```go
// While a Prompt is running in another goroutine:
_, err := session.Prompt(ctx, "Use this additional screenshot", codingagent.PromptOptions{
    Images: images,
    StreamingBehavior: "steer",
})

err = session.FollowUp("Then inspect this image", codingagent.PromptOptions{
    Images: images,
})
```

`"steer"` delivers at the next turn boundary; `"followUp"` delivers when the
agent would stop. A queued `Prompt` returns an empty `RunResult`
immediately, not a completed task result. Subscribe to the running session for
delivery and completion events. Without a streaming behavior, concurrent prompts
return `ErrAgentSessionBusy`. Invalid behavior strings and cancelled contexts
are rejected before queuing. Manual compaction rejects input while it runs.

`Steer` and `FollowUp` accept images and the expansion
toggle, but reject `StreamingBehavior`: their method names already select the
queue. Pending queue order/content survives in-memory retry, compaction and
model/tool rebuilds. Undelivered queues do not survive process restarts; consumed
messages are saved as ordinary transcript entries. Queue submission contexts
are checked at submission and do not cancel the independently running prompt.

## Verification and ongoing porting

`prompt_options_test.go` uses a local HTTP/SSE server to verify actual native
Chat Completions image requests, a transient failure and JSONL reopen. Offline
tests also cover both queue paths, image snapshot isolation, multiple pending
messages across retry/model changes, expansion, cancellation, busy and closed
sessions. `sdk_services_test.go` checks prepared resource isolation, observer
forwarding and virtual model routes. No test calls a paid model.

`migration/sync.json` maps the new implementation/tests back to Pi's source;
`source_map_session.json` describes the Go API adaptation. The upstream revision
has not advanced. These are manual maintenance repairs, not new Portsmith
migration completion receipts. Frozen judges and historical receipts are
unchanged. Follow-up ports must preserve these repairs and rerun the regressions.
