package utils_test

import (
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
	"reflect"
	"testing"
)

func TestPortsmithJudgePiV1HeaderMerge(t *testing.T) {
	fn := reflect.ValueOf(utils.ProviderHeadersToRecord)
	if !fn.Type().IsVariadic() {
		t.Fatal("Pi 1.0 requires variadic case-insensitive provider header merging")
	}
	a, b, c := "first", "second", "last"
	headers := []types.ProviderHeaders{{"X-Token": &a, "Keep": &a}, {"x-token": &b, "KEEP": nil}, {"X-TOKEN": nil, "Another": &c}}
	out := fn.CallSlice([]reflect.Value{reflect.ValueOf(headers)})[0].Interface().(map[string]string)
	if !reflect.DeepEqual(out, map[string]string{"Another": "last"}) {
		t.Fatalf("case-insensitive overrides/null deletion: %v", out)
	}
}

func TestPortsmithJudgePiV1ProviderErrors(t *testing.T) {
	for _, test := range []struct {
		message string
		retry   bool
	}{
		{"subscription_sharing_usage_limit_exceeded", false},
		{"subscription_sharing_usage_unavailable", true},
		{"subscription_sharing_user_unavailable", true},
		{"billing quota exceeded", false},
	} {
		message := types.NewAssistantMessage(types.ApiOpenAICompletions, types.ProviderOpenAI, "fixture", 1)
		message.StopReason = types.StopReasonError
		message.ErrorMessage = &test.message
		if actual := utils.IsRetryableAssistantError(message); actual != test.retry {
			t.Errorf("retry classification %q: %v", test.message, actual)
		}
	}
	text := `{"code":"1261","message":"Prompt exceeds max length"}`
	message := types.NewAssistantMessage(types.ApiOpenAICompletions, types.ProviderId("zai-cn"), "fixture", 1)
	message.StopReason = types.StopReasonError
	message.ErrorMessage = &text
	if !utils.IsContextOverflow(message, nil) {
		t.Fatal("Z.AI CN context overflow must trigger compaction rather than an unrelated retry")
	}
}
