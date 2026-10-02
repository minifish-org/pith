// Prompt cache warming for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/cache-warmer.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. Warming re-sends a
// request with a one-token output cap just before its prompt cache entry
// expires, so the next real request hits the cache.
//
// Warming is opt-in, bounded and cancellable. It only ever sends a request
// through an injected Warm callback and only when a caller explicitly Start()s
// it, so constructing a CacheWarmer never issues a paid request. The schedule is
// injected, so tests control time without real timers.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"sync"
	"time"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// Cache warming bounds and constants.
const (
	// CacheWarmingMaxAgeMS bounds streaming warming after the real request.
	CacheWarmingMaxAgeMS = 60 * 60_000
	// CacheWarmingMaxIdleAgeMS bounds idle warming.
	CacheWarmingMaxIdleAgeMS = 30 * 60_000
	// CacheWarmingMinimumExpectedSavings is the dollar threshold for a refresh.
	CacheWarmingMinimumExpectedSavings = 0.05
	// CacheWarmingIdleContinuationProbability is the estimated chance a real
	// request arrives before the entry expires.
	CacheWarmingIdleContinuationProbability = 0.15
)

// CacheWarmingAction is the warmer's decision for one refresh.
type CacheWarmingAction string

// Cache warming actions.
const (
	CacheWarmingActionWarm CacheWarmingAction = "warm"
	CacheWarmingActionStop CacheWarmingAction = "stop"
)

// CacheWarmingDecision is the economics of one warm-or-stop decision.
type CacheWarmingDecision struct {
	Phase                   string
	WarmCost                float64
	MissCost                float64
	ContinuationProbability float64
	ExpectedSavings         float64
	EconomicsAvailable      bool
	Action                  CacheWarmingAction
}

// CacheWarmingStatus is the current warmer state.
type CacheWarmingStatus struct {
	State      string
	Reason     string
	NextWarmAt int64
	Decision   *CacheWarmingDecision
}

// CacheWarmRequest is the request whose cache entry should stay warm, exactly
// as it was sent.
type CacheWarmRequest struct {
	Model   aitypes.Model
	Context aitypes.TranscriptContext
	Options *aitypes.SimpleStreamOptions
}

// CacheWarmSchedule returns a channel that fires after delay. It is injected so
// tests can control time.
type CacheWarmSchedule func(ctx context.Context, delay time.Duration) <-chan time.Time

// CacheWarmFn performs the warm request and returns its usage.
type CacheWarmFn func(ctx context.Context, request CacheWarmRequest) (aitypes.Usage, error)

// CacheWarmingOptions configures a CacheWarmer.
type CacheWarmingOptions struct {
	// Mode reports the current setting; nil means off.
	Mode func() CacheWarmingMode
	// Schedule fires the refresh timer. Defaults to a real timer.
	Schedule CacheWarmSchedule
	// Warm sends the refresh. Without it, warming is disabled.
	Warm CacheWarmFn
	// Decide may override the action. It defaults to Pi's decision.
	Decide func(CacheWarmingDecision) CacheWarmingAction
	// Now is the clock, for tests.
	Now func() time.Time
	// OnWarmed receives the usage after each successful refresh.
	OnWarmed func(aitypes.Usage)
}

// CacheWarmer keeps one prompt cache entry alive.
type CacheWarmer struct {
	mu       sync.Mutex
	options  CacheWarmingOptions
	run      *cacheWarmRun
	inactive CacheWarmingStatus
	cancel   context.CancelFunc
}

type cacheWarmRun struct {
	request        CacheWarmRequest
	ttlMs          int64
	delayMs        int64
	nextWarmAt     int64
	startedAt      int64
	phase          string
	extensionForce bool
}

// NewCacheWarmer builds a warmer. It performs no work until Start.
func NewCacheWarmer(options CacheWarmingOptions) *CacheWarmer {
	if options.Schedule == nil {
		options.Schedule = func(ctx context.Context, delay time.Duration) <-chan time.Time {
			timer := time.NewTimer(delay)
			go func() {
				<-ctx.Done()
				timer.Stop()
			}()
			return timer.C
		}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &CacheWarmer{options: options, inactive: CacheWarmingStatus{State: "inactive", Reason: "waiting for first request"}}
}

// GetCacheWarmingDelayMs returns the refresh delay for a TTL. A TTL at or below
// ten seconds is too short to warm.
func GetCacheWarmingDelayMs(ttlMs int64) (int64, bool) {
	if ttlMs <= 10_000 {
		return 0, false
	}
	delay := ttlMs * 9 / 10
	if ttlMs-10_000 < delay {
		delay = ttlMs - 10_000
	}
	if delay < 1 {
		delay = 1
	}
	return delay, true
}

// GetPromptCacheTTLMs returns the lifetime of the prompt cache entry a request
// writes, or (0, false) when caching is off or the model has no lifetime.
func GetPromptCacheTTLMs(model aitypes.Model, options *aitypes.SimpleStreamOptions) (int64, bool) {
	retention := aitypes.CacheRetentionShort
	if options != nil && options.CacheRetention != nil {
		retention = *options.CacheRetention
	}
	if retention == aitypes.CacheRetentionNone {
		return 0, false
	}
	if model.PromptCache == nil {
		return 0, false
	}
	seconds, ok := model.PromptCache.Get(retention)
	if !ok {
		return 0, false
	}
	return int64(seconds * 1000), true
}

// IsCacheWarmReplayable reports whether replaying a request with a one-token
// output cap leaves its cache entry untouched.
func IsCacheWarmReplayable(model aitypes.Model, options *aitypes.SimpleStreamOptions) bool {
	if options == nil || options.Reasoning == nil || model.Api != aitypes.ApiAnthropicMessages {
		return true
	}
	if model.Compat.AnthropicMessages != nil && model.Compat.AnthropicMessages.ForceAdaptiveThinking != nil {
		return *model.Compat.AnthropicMessages.ForceAdaptiveThinking
	}
	return false
}

// CacheWarmingEconomics computes the decision for one run. promptTokens is the
// most recent real request's prompt size.
func CacheWarmingEconomics(model aitypes.Model, promptTokens float64, phase string) CacheWarmingDecision {
	continuation := 1.0
	if phase == "idle" {
		continuation = CacheWarmingIdleContinuationProbability
	}
	decision := CacheWarmingDecision{Phase: phase, ContinuationProbability: continuation, Action: CacheWarmingActionWarm}
	if promptTokens <= 0 {
		return decision
	}
	cacheHitCost := modelCost(model, 0, promptTokens, 0)
	var cacheMissCost float64
	if model.Cost.CacheWrite > 0 {
		cacheMissCost = model.Cost.CacheWrite * promptTokens / 1_000_000
	} else {
		cacheMissCost = modelCost(model, promptTokens, 0, 0)
	}
	warmCost := modelCost(model, 0, promptTokens, 1)
	missCost := cacheMissCost - cacheHitCost
	if missCost < 0 {
		missCost = 0
	}
	decision.WarmCost = warmCost
	decision.MissCost = missCost
	decision.EconomicsAvailable = cacheHitCost > 0 || cacheMissCost > 0
	decision.ExpectedSavings = continuation*missCost - warmCost
	if decision.ExpectedSavings < CacheWarmingMinimumExpectedSavings {
		decision.Action = CacheWarmingActionStop
	}
	return decision
}

// modelCost prices the given token usage against a model.
func modelCost(model aitypes.Model, input, cacheRead, output float64) float64 {
	rates := model.Cost.ModelCostRates
	for _, tier := range model.Cost.Tiers {
		if input > tier.InputTokensAbove {
			rates = tier.ModelCostRates
		}
	}
	return (input*rates.Input + cacheRead*rates.CacheRead + output*rates.Output) / 1_000_000
}

// lastPromptTokens returns the prompt size of the most recent assistant message
// in the request context.
func lastPromptTokens(request CacheWarmRequest) float64 {
	for index := len(request.Context.Messages) - 1; index >= 0; index-- {
		assistant := request.Context.Messages[index].Assistant
		if assistant == nil {
			continue
		}
		return assistant.Usage.Input + assistant.Usage.CacheRead + assistant.Usage.CacheWrite
	}
	return 0
}

// Start replaces any previous run. It returns false when warming is disabled,
// the mode is off, or the request cannot be replayed.
func (w *CacheWarmer) Start(request CacheWarmRequest) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked()
	if w.options.Mode == nil || w.options.Mode() == CacheWarmingOff || w.options.Warm == nil {
		w.inactive = CacheWarmingStatus{State: "inactive", Reason: "cache warming disabled"}
		return false
	}
	if !IsCacheWarmReplayable(request.Model, request.Options) {
		w.inactive = CacheWarmingStatus{State: "inactive", Reason: "request is not replayable"}
		return false
	}
	ttlMs, ok := GetPromptCacheTTLMs(request.Model, request.Options)
	if !ok {
		w.inactive = CacheWarmingStatus{State: "inactive", Reason: "cache economics unavailable"}
		return false
	}
	delayMs, ok := GetCacheWarmingDelayMs(ttlMs)
	if !ok {
		w.inactive = CacheWarmingStatus{State: "inactive", Reason: "cache ttl too short"}
		return false
	}
	nowMs := w.options.Now().UnixMilli()
	w.run = &cacheWarmRun{
		request:    request,
		ttlMs:      ttlMs,
		delayMs:    delayMs,
		nextWarmAt: nowMs + delayMs,
		startedAt:  nowMs,
		phase:      "streaming",
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go w.wait(ctx, delayMs)
	return true
}

func (w *CacheWarmer) wait(ctx context.Context, delayMs int64) {
	ch := w.options.Schedule(ctx, time.Duration(delayMs)*time.Millisecond)
	select {
	case <-ctx.Done():
		return
	case <-ch:
	}
	w.mu.Lock()
	run := w.run
	if run == nil {
		w.mu.Unlock()
		return
	}
	maxAge := int64(CacheWarmingMaxAgeMS)
	if run.phase == "idle" {
		maxAge = CacheWarmingMaxIdleAgeMS
	}
	if w.options.Now().UnixMilli()-run.startedAt > maxAge {
		w.inactive = CacheWarmingStatus{State: "inactive", Reason: "warming age exceeded"}
		w.mu.Unlock()
		return
	}
	decision := CacheWarmingEconomics(run.request.Model, lastPromptTokens(run.request), run.phase)
	action := decision.Action
	if w.options.Decide != nil {
		if override := w.options.Decide(decision); override != "" {
			action = override
		}
	}
	decision.Action = action
	warm := action == CacheWarmingActionWarm
	onWarmed := w.options.OnWarmed
	warmFn := w.options.Warm
	request := run.request
	if !warm {
		w.inactive = CacheWarmingStatus{State: "inactive", Reason: "warming stopped", Decision: &decision}
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	usage, err := warmFn(ctx, request)
	if err != nil {
		return
	}
	if onWarmed != nil {
		onWarmed(usage)
	}
}

// Stop cancels any scheduled or in-flight refresh.
func (w *CacheWarmer) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked()
	w.inactive = CacheWarmingStatus{State: "inactive", Reason: "stopped"}
}

func (w *CacheWarmer) stopLocked() {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.run = nil
}

// Status returns the current warmer state.
func (w *CacheWarmer) Status() CacheWarmingStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.options.Mode != nil && w.options.Mode() == CacheWarmingOff {
		return CacheWarmingStatus{State: "inactive", Reason: "cache warming disabled"}
	}
	if w.run == nil {
		return w.inactive
	}
	decision := CacheWarmingEconomics(w.run.request.Model, lastPromptTokens(w.run.request), w.run.phase)
	return CacheWarmingStatus{State: "scheduled", NextWarmAt: w.run.nextWarmAt, Decision: &decision}
}

// SetPhase updates the phase of the active run. It is a no-op without a run.
func (w *CacheWarmer) SetPhase(phase string) {
	w.mu.Lock()
	if w.run != nil {
		w.run.phase = phase
	}
	w.mu.Unlock()
}
