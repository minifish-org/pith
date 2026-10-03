package harness

import (
	"context"
	"encoding/json"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable"
)

// UsageDoc is the built-in per-conversation usage ledger.
var UsageDoc, _ = durable.DefineDoc(durable.DocumentDefinition{
	Kind:    "pi.usage",
	Version: 1,
	Scope:   durable.ScopeConversation,
	History: durable.HistoryLatest,
	Fork:    durable.ForkInitial,
	Initial: func(json.RawMessage) (durable.JsonObject, error) {
		return durable.JsonObject{"models": map[string]any{}, "tools": map[string]any{}}, nil
	},
	CheckpointWhen: func(durable.JsonObject, []chordOp, durable.CheckpointInfo) (bool, error) {
		return true, nil
	},
})

func usageAddress(conversationID durable.ConversationID) durable.DocumentAddress {
	return durable.ConversationAddress(UsageDoc, conversationID, nil)
}

// recordUsage adds usage to one bucket of the conversation's usage doc.
func recordUsage(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, bucket, key string, usage types.Usage) error {
	draft, err := tx.Doc(ctx, *UsageDoc, usageAddress(conversationID), nil)
	if err != nil {
		return err
	}
	state, err := draft.Value()
	if err != nil {
		return err
	}
	totals, ok := state[bucket].(map[string]any)
	if !ok {
		totals = map[string]any{}
		state[bucket] = totals
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return err
	}
	if existing, ok := totals[key].(map[string]any); ok {
		addUsageMap(existing, value)
	} else {
		totals[key] = value
	}
	return nil
}

func addUsageMap(total map[string]any, usage map[string]any) {
	addNumber(total, usage, "input")
	addNumber(total, usage, "output")
	addNumber(total, usage, "cacheRead")
	addNumber(total, usage, "cacheWrite")
	addNumber(total, usage, "totalTokens")
	addOptionalNumber(total, usage, "cacheWrite1h")
	addOptionalNumber(total, usage, "reasoning")
	totalCost, _ := total["cost"].(map[string]any)
	if totalCost == nil {
		totalCost = map[string]any{}
		total["cost"] = totalCost
	}
	usageCost, _ := usage["cost"].(map[string]any)
	for _, field := range []string{"input", "output", "cacheRead", "cacheWrite", "total"} {
		addNumber(totalCost, usageCost, field)
	}
}

func addNumber(total map[string]any, usage map[string]any, key string) {
	if usage == nil {
		return
	}
	value, _ := toNumber(usage[key])
	current, _ := toNumber(total[key])
	total[key] = current + value
}

func addOptionalNumber(total map[string]any, usage map[string]any, key string) {
	if usage == nil {
		return
	}
	if _, ok := usage[key]; !ok {
		return
	}
	value, _ := toNumber(usage[key])
	current, _ := toNumber(total[key])
	total[key] = current + value
}

// addUsageState adds every bucket of state into sum.
func addUsageState(sum UsageState, state UsageState) {
	for key, usage := range state.Models {
		if existing, ok := sum.Models[key]; ok {
			sum.Models[key] = addUsageValue(existing, usage)
		} else {
			sum.Models[key] = usage
		}
	}
	for key, usage := range state.Tools {
		if existing, ok := sum.Tools[key]; ok {
			sum.Tools[key] = addUsageValue(existing, usage)
		} else {
			sum.Tools[key] = usage
		}
	}
}

func addUsageValue(total types.Usage, usage types.Usage) types.Usage {
	total.Input += usage.Input
	total.Output += usage.Output
	total.CacheRead += usage.CacheRead
	total.CacheWrite += usage.CacheWrite
	total.TotalTokens += usage.TotalTokens
	if usage.CacheWrite1h != nil {
		value := *usage.CacheWrite1h
		if total.CacheWrite1h != nil {
			value += *total.CacheWrite1h
		}
		total.CacheWrite1h = &value
	}
	if usage.Reasoning != nil {
		value := *usage.Reasoning
		if total.Reasoning != nil {
			value += *total.Reasoning
		}
		total.Reasoning = &value
	}
	total.Cost.Input += usage.Cost.Input
	total.Cost.Output += usage.Cost.Output
	total.Cost.CacheRead += usage.Cost.CacheRead
	total.Cost.CacheWrite += usage.Cost.CacheWrite
	total.Cost.Total += usage.Cost.Total
	return total
}

// decodeUsageState decodes a committed pi.usage document into a UsageState.
func decodeUsageState(value durable.JsonObject) UsageState {
	state := UsageState{Models: map[string]types.Usage{}, Tools: map[string]types.Usage{}}
	if value == nil {
		return state
	}
	decodeUsageBucket(value["models"], state.Models)
	decodeUsageBucket(value["tools"], state.Tools)
	return state
}

func decodeUsageBucket(raw any, target map[string]types.Usage) {
	bucket, ok := raw.(map[string]any)
	if !ok {
		return
	}
	for key, value := range bucket {
		data, err := json.Marshal(value)
		if err != nil {
			continue
		}
		var usage types.Usage
		if err := json.Unmarshal(data, &usage); err != nil {
			continue
		}
		target[key] = usage
	}
}
