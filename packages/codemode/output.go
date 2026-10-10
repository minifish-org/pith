package codemode

import "unicode/utf8"

// BudgetTextOutput retains a UTF-8-safe prefix of the text items, sharing one
// estimated budget across all items. One token allows four UTF-8 bytes; this is
// not a provider tokenizer or a limit on image data or tool execution. Images
// retain their positions even after the text budget is exhausted. The input
// slice is never modified. A zero budget suppresses text, not images.
func BudgetTextOutput(items []OutputItem, maxTokens int64) ([]OutputItem, bool) {
	remaining := int64(0)
	if maxTokens > 0 {
		// Public callers need not use the source parser's safe-integer bound.
		const maxInt64 = int64(1<<63 - 1)
		if maxTokens > maxInt64/4 {
			remaining = maxInt64
		} else {
			remaining = maxTokens * 4
		}
	}
	output := make([]OutputItem, 0, len(items))
	truncated := false
	for _, item := range items {
		if item.Type != "text" {
			output = append(output, item)
			continue
		}
		if int64(len(item.Text)) > remaining {
			cut := int(remaining)
			for cut > 0 && !utf8.RuneStart(item.Text[cut]) {
				cut--
			}
			item.Text = item.Text[:cut]
			truncated = true
			// An incomplete code point must not let later text jump ahead of
			// omitted content: the retained text is always one ordered prefix.
			remaining = 0
		} else {
			remaining -= int64(len(item.Text))
		}
		if item.Text != "" {
			output = append(output, item)
		}
	}
	return output, truncated
}
