// This file carries testing/conformance/session-repo.ts: reusable repository
// conformance cases.
package conformance

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	sessiontesting "github.com/minifish-org/pith/packages/agent/harness/session/testing"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

const (
	rootID      = "00000000-0000-7000-8000-000000000001"
	childID     = "00000000-0000-7000-8000-000000000002"
	siblingID   = "00000000-0000-7000-8000-000000000003"
	usageID     = "00000000-0000-7000-8000-000000000004"
	operationID = "00000000-0000-7000-8000-000000000005"
	pendingID   = "00000000-0000-7000-8000-000000000006"
	unknownID   = "00000000-0000-7000-8000-000000000007"
)

// SessionRepoBackend is the structural repository surface the conformance
// cases need. Concrete repositories are adapted by the caller.
type SessionRepoBackend[T harnesstypes.SessionMetadataView] interface {
	Create(id string, parentSessionID *string) (harnesstypes.Session[T], error)
	Open(metadata T) (harnesstypes.Session[T], error)
	List() ([]T, error)
	Delete(metadata T) error
	Fork(source T, options harnesstypes.ForkOptions) (harnesstypes.Session[T], error)
}

// SessionRepoConformanceFactory builds one backend and its close callback.
type SessionRepoConformanceFactory[T harnesstypes.SessionMetadataView] func() (SessionRepoBackend[T], func() error)

func configuration() harnesstypes.LaneConfiguration {
	return harnesstypes.LaneConfiguration{
		Model:           harnesstypes.ModelIdentity{Provider: "provider", ModelID: "model"},
		ThinkingLevel:   agenttypes.ThinkingOff,
		ActiveToolNames: []string{"read"},
	}
}

func idleLaneState() harnesstypes.LaneState {
	return harnesstypes.LaneState{Inbox: []harnesstypes.InboxItem{}}
}

var applicationValue = session.NewValue("test.application.value")
var applicationList = session.NewList("test.application.list")

func branch(name string) harnesstypes.Value     { return session.BranchTip(name) }
func laneConfig(name string) harnesstypes.Value { return session.LaneConfig(name) }
func laneState(name string) harnesstypes.Value  { return session.LaneState(name) }

func assistantMessage(stopReason aitypes.StopReason) agenttypes.AgentMessage {
	message := aitypes.NewAssistantMessage(aitypes.Api("anthropic-messages"), aitypes.ProviderId("anthropic"), "claude-sonnet-4-5", 1)
	if stopReason == aitypes.StopReasonToolUse {
		message.Content = []aitypes.ContentBlock{aitypes.ToolCallBlock(aitypes.NewToolCall("call", "read", json.RawMessage("{}")))}
	} else {
		message.Content = []aitypes.ContentBlock{aitypes.TextBlock(string(stopReason))}
	}
	message.StopReason = stopReason
	if stopReason == aitypes.StopReasonDeferred {
		message.Deferred = &aitypes.DeferredHandle{Provider: "anthropic", ModelId: "claude-sonnet-4-5", Api: "anthropic-messages", Id: "job"}
	}
	return agenttypes.NewAgentMessageFromMessage(aitypes.NewAssistantMessageVariant(message))
}

func sessionMutate[T harnesstypes.SessionMetadataView](s harnesstypes.Session[T], fn func(m harnesstypes.SessionMutator) error) error {
	mutator, err := s.BeginMutation(ctx)
	if err != nil {
		return err
	}
	defer mutator.End(ctx)
	return fn(mutator)
}

func getBranchTip[T harnesstypes.SessionMetadataView](s harnesstypes.Session[T], name string) (*string, bool, error) {
	br, ok, err := s.Branch(name, ctx)
	if err != nil || !ok {
		return nil, false, err
	}
	tip, err := br.GetTipID(ctx)
	if err != nil {
		return nil, false, err
	}
	return tip, true, nil
}

func repoCase[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T], group, name string, test func(SessionRepoBackend[T]) error) sessiontesting.ConformanceCase {
	return sessiontesting.ConformanceCase{Group: group, Name: name, Run: func() error {
		backend, closeFn := factory()
		if closeFn != nil {
			defer closeFn()
		}
		return test(backend)
	}}
}

// CreateSessionRepoLifecycleConformance creates lifecycle cases.
func CreateSessionRepoLifecycleConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	return []sessiontesting.ConformanceCase{
		repoCase(factory, "lifecycle", "creates a session with no implicit branch and rejects duplicate ids", func(repo SessionRepoBackend[T]) error {
			created, err := repo.Create("session", nil)
			if err != nil {
				return err
			}
			if created.Metadata().SessionID() != "session" {
				return fmt.Errorf("unexpected id")
			}
			if _, ok, err := created.Branch("main", ctx); err != nil || ok {
				return fmt.Errorf("expected no implicit branch")
			}
			if _, ok, err := created.GetValue(laneState("main"), ctx); err != nil || ok {
				return fmt.Errorf("expected no lane state")
			}
			if _, ok, err := created.GetValue(laneConfig("main"), ctx); err != nil || ok {
				return fmt.Errorf("expected no lane config")
			}
			if _, err := repo.Create("session", nil); err == nil {
				return fmt.Errorf("expected duplicate id to reject")
			}
			return created.Close(ctx)
		}),
		repoCase(factory, "lifecycle", "close drains an acquired scope and rejects a queued mutation callback", func(repo SessionRepoBackend[T]) error {
			created, err := repo.Create("session", nil)
			if err != nil {
				return err
			}
			active, err := created.BeginMutation(ctx)
			if err != nil {
				return err
			}
			queuedStarted := false
			closing := make(chan error, 1)
			go func() { closing <- created.Close(ctx) }()
			// Close seals admission synchronously before it drains; yield so the
			// queued mutation is admitted against a closing session.
			time.Sleep(10 * time.Millisecond)
			queuedDone := make(chan error, 1)
			go func() {
				_, err := created.Mutate(func(m harnesstypes.SessionMutator, c harnesstypes.Context) (any, error) {
					queuedStarted = true
					return nil, nil
				}, ctx)
				queuedDone <- err
			}()
			if err := active.End(ctx); err != nil {
				return err
			}
			if err := <-queuedDone; err == nil {
				return fmt.Errorf("expected the queued mutation to reject")
			}
			if err := <-closing; err != nil {
				return err
			}
			if queuedStarted {
				return fmt.Errorf("queued mutation must not run")
			}
			return nil
		}),
		repoCase(factory, "lifecycle", "lists metadata and preserves state across close and reopen", func(repo SessionRepoBackend[T]) error {
			first, err := repo.Create("first", nil)
			if err != nil {
				return err
			}
			name := "preserved"
			if err := first.SetName(&name, ctx); err != nil {
				return err
			}
			parent := "parent"
			second, err := repo.Create("second", &parent)
			if err != nil {
				return err
			}
			if err := second.Close(ctx); err != nil {
				return err
			}
			listed, err := repo.List()
			if err != nil {
				return err
			}
			if len(listed) != 2 {
				return fmt.Errorf("expected 2 sessions, got %d", len(listed))
			}
			if err := first.Close(ctx); err != nil {
				return err
			}
			if _, err := first.GetName(ctx); err == nil {
				return fmt.Errorf("expected closed read to reject")
			}
			reopened, err := repo.Open(first.Metadata())
			if err != nil {
				return err
			}
			gotName, err := reopened.GetName(ctx)
			if err != nil {
				return err
			}
			if gotName == nil || *gotName != "preserved" {
				return fmt.Errorf("expected preserved name")
			}
			return reopened.Close(ctx)
		}),
		repoCase(factory, "lifecycle", "deletes closed sessions without affecting other sessions", func(repo SessionRepoBackend[T]) error {
			removed, err := repo.Create("removed", nil)
			if err != nil {
				return err
			}
			retained, err := repo.Create("retained", nil)
			if err != nil {
				return err
			}
			if err := removed.Close(ctx); err != nil {
				return err
			}
			if err := retained.Close(ctx); err != nil {
				return err
			}
			if err := repo.Delete(removed.Metadata()); err != nil {
				return err
			}
			listed, err := repo.List()
			if err != nil {
				return err
			}
			if len(listed) != 1 || listed[0].SessionID() != "retained" {
				return fmt.Errorf("unexpected listing %v", listed)
			}
			if _, err := repo.Open(removed.Metadata()); err == nil {
				return fmt.Errorf("expected removed open to reject")
			}
			if err := repo.Delete(removed.Metadata()); err == nil {
				return fmt.Errorf("expected removed delete to reject")
			}
			return nil
		}),
	}
}

// CreateSessionRepoOwnershipConformance creates exclusive-open cases.
func CreateSessionRepoOwnershipConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	return []sessiontesting.ConformanceCase{
		repoCase(factory, "ownership", "rejects opening an already-open session", func(repo SessionRepoBackend[T]) error {
			created, err := repo.Create("session", nil)
			if err != nil {
				return err
			}
			if _, err := repo.Open(created.Metadata()); err == nil {
				return fmt.Errorf("expected already-open to reject")
			}
			if err := created.Close(ctx); err != nil {
				return err
			}
			reopened, err := repo.Open(created.Metadata())
			if err != nil {
				return err
			}
			if _, err := repo.Open(created.Metadata()); err == nil {
				return fmt.Errorf("expected second open to reject")
			}
			return reopened.Close(ctx)
		}),
	}
}

// CreateSessionRepoMessageConformance creates message cases.
func CreateSessionRepoMessageConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	return []sessiontesting.ConformanceCase{
		repoCase(factory, "messages", "rejects pending assistant messages without changing the tree", func(repo SessionRepoBackend[T]) error {
			created, err := repo.Create("session", nil)
			if err != nil {
				return err
			}
			br, err := created.CreateBranch("main", nil, ctx)
			if err != nil {
				return err
			}
			if _, err := br.AppendMessage(assistantMessage(aitypes.StopReasonPending), ctx); err == nil {
				return fmt.Errorf("expected pending assistant message to reject")
			}
			tip, ok, err := getBranchTip(created, "main")
			if err != nil {
				return err
			}
			if !ok || tip != nil {
				return fmt.Errorf("expected null tip")
			}
			entries, err := created.FindEntries(nil, ctx)
			if err != nil {
				return err
			}
			if len(entries) != 0 {
				return fmt.Errorf("expected empty tree")
			}
			return created.Close(ctx)
		}),
		repoCase(factory, "messages", "preserves every settled assistant stop reason", func(repo SessionRepoBackend[T]) error {
			created, err := repo.Create("session", nil)
			if err != nil {
				return err
			}
			br, err := created.CreateBranch("main", nil, ctx)
			if err != nil {
				return err
			}
			reasons := []aitypes.StopReason{aitypes.StopReasonStop, aitypes.StopReasonLength, aitypes.StopReasonToolUse, aitypes.StopReasonError, aitypes.StopReasonAborted, aitypes.StopReasonDeferred}
			messages := make([]agenttypes.AgentMessage, 0, len(reasons))
			ids := []string{}
			for _, reason := range reasons {
				message := assistantMessage(reason)
				messages = append(messages, message)
				id, err := br.AppendMessage(message, ctx)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			entries, err := created.FindEntries(&harnesstypes.EntryQuery{Order: orderPointer("asc"), Type: entryTypePointer(harnesstypes.EntryTypeMessage)}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(ids, entryIDs(entries), "entry ids"); err != nil {
				return err
			}
			for index, entry := range entries {
				messageEntry, ok := entry.(harnesstypes.MessageEntry)
				if !ok {
					return fmt.Errorf("expected message entry")
				}
				if err := assertEqual(messages[index], messageEntry.Message, "message"); err != nil {
					return err
				}
			}
			tip, _, err := getBranchTip(created, "main")
			if err != nil {
				return err
			}
			if tip == nil || *tip != ids[len(ids)-1] {
				return fmt.Errorf("unexpected tip")
			}
			return created.Close(ctx)
		}),
	}
}

// CreateSessionRepoForkBehaviorConformance creates fork-content cases.
func CreateSessionRepoForkBehaviorConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	return []sessiontesting.ConformanceCase{
		repoCase(factory, "forks", "tree-forks a fresh session before first attachment", func(repo SessionRepoBackend[T]) error {
			source, err := repo.Create("source", nil)
			if err != nil {
				return err
			}
			fork, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "tree", ID: stringPointer("fork")})
			if err != nil {
				return err
			}
			if fork.Metadata().SessionID() != "fork" {
				return fmt.Errorf("unexpected fork id")
			}
			if _, ok, err := fork.Branch("main", ctx); err != nil || ok {
				return fmt.Errorf("expected no branch")
			}
			entries, err := fork.FindEntries(nil, ctx)
			if err != nil {
				return err
			}
			if len(entries) != 0 {
				return fmt.Errorf("expected empty tree")
			}
			stats, err := fork.GetStats(ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(harnesstypes.SessionStats{MessageCount: 0, Usage: zeroUsage()}, stats, "stats"); err != nil {
				return err
			}
			_ = source.Close(ctx)
			return fork.Close(ctx)
		}),
		repoCase(factory, "forks", "rejects a data-only branch and releases its destination id", func(repo SessionRepoBackend[T]) error {
			source, err := repo.Create("source", nil)
			if err != nil {
				return err
			}
			if _, err := source.CreateBranch("data", nil, ctx); err != nil {
				return err
			}
			if _, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("data"), ID: stringPointer("destination")}); err == nil {
				return fmt.Errorf("expected data-only branch fork to reject")
			}
			listed, err := repo.List()
			if err != nil {
				return err
			}
			if len(listed) != 1 || listed[0].SessionID() != "source" {
				return fmt.Errorf("destination id was not released")
			}
			destination, err := repo.Create("destination", nil)
			if err != nil {
				return err
			}
			_ = source.Close(ctx)
			return destination.Close(ctx)
		}),
		repoCase(factory, "forks", "enforces branch ancestry for at and before placement", func(repo SessionRepoBackend[T]) error {
			source, err := repo.Create("source", nil)
			if err != nil {
				return err
			}
			if err := sessionMutate(source, func(m harnesstypes.SessionMutator) error {
				_, err := m.Commit([]harnesstypes.Write{
					session.InsertEntry(customEntry(rootID, nil, "root", nil)),
					session.InsertEntry(customEntry(childID, stringPointer(rootID), "child", nil)),
					session.InsertEntry(customEntry(siblingID, stringPointer(rootID), "sibling", nil)),
					session.SetValue(branch("main"), childID),
					session.SetValue(laneConfig("main"), configuration()),
					session.SetValue(laneState("main"), idleLaneState()),
					session.SetValue(branch("empty"), nil),
					session.SetValue(laneConfig("empty"), configuration()),
					session.SetValue(laneState("empty"), idleLaneState()),
				}, ctx)
				return err
			}); err != nil {
				return err
			}
			before, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("main"), EntryID: stringPointer(childID), Position: stringPointer("before"), ID: stringPointer("before")})
			if err != nil {
				return err
			}
			tip, _, err := getBranchTip(before, "main")
			if err != nil {
				return err
			}
			if tip == nil || *tip != rootID {
				return fmt.Errorf("unexpected before tip")
			}
			mid, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("main"), EntryID: stringPointer(rootID), Position: stringPointer("at"), ID: stringPointer("mid")})
			if err != nil {
				return err
			}
			tip, _, err = getBranchTip(mid, "main")
			if err != nil {
				return err
			}
			if tip == nil || *tip != rootID {
				return fmt.Errorf("unexpected mid tip")
			}
			beforeRoot, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("main"), EntryID: stringPointer(rootID), Position: stringPointer("before"), ID: stringPointer("before-root")})
			if err != nil {
				return err
			}
			tip, ok, err := getBranchTip(beforeRoot, "main")
			if err != nil {
				return err
			}
			if !ok || tip != nil {
				return fmt.Errorf("unexpected before-root tip")
			}
			for _, invalid := range []struct {
				id      string
				branch  string
				entryID string
			}{
				{"off-branch", "main", siblingID},
				{"unknown", "main", unknownID},
				{"null-tip", "empty", rootID},
			} {
				if _, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer(invalid.branch), EntryID: stringPointer(invalid.entryID), ID: stringPointer(invalid.id)}); err == nil {
					return fmt.Errorf("expected %s to reject", invalid.id)
				}
			}
			return nil
		}),
		repoCase(factory, "forks", "forks a closed source session", func(repo SessionRepoBackend[T]) error {
			source, err := repo.Create("source", nil)
			if err != nil {
				return err
			}
			if err := sessionMutate(source, func(m harnesstypes.SessionMutator) error {
				_, err := m.Commit([]harnesstypes.Write{
					session.InsertEntry(customEntry(rootID, nil, "root", nil)),
					session.SetValue(branch("main"), rootID),
					session.SetValue(laneConfig("main"), configuration()),
					session.SetValue(laneState("main"), idleLaneState()),
					session.SetValue(applicationValue, "excluded"),
					session.AppendList(applicationList, "excluded"),
				}, ctx)
				return err
			}); err != nil {
				return err
			}
			if err := source.Close(ctx); err != nil {
				return err
			}
			fork, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("main"), ID: stringPointer("fork")})
			if err != nil {
				return err
			}
			tip, _, err := getBranchTip(fork, "main")
			if err != nil {
				return err
			}
			if tip == nil || *tip != rootID {
				return fmt.Errorf("unexpected tip")
			}
			if _, ok, err := fork.GetValue(applicationValue, ctx); err != nil || ok {
				return fmt.Errorf("application value should be excluded")
			}
			elements, err := fork.ReadList(applicationList, nil, ctx)
			if err != nil {
				return err
			}
			if len(elements) != 0 {
				return fmt.Errorf("application list should be excluded")
			}
			return fork.Close(ctx)
		}),
		repoCase(factory, "forks", "forks the whole configured tree with fresh lane state", func(repo SessionRepoBackend[T]) error {
			source, err := repo.Create("source", nil)
			if err != nil {
				return err
			}
			if err := sessionMutate(source, func(m harnesstypes.SessionMutator) error {
				_, err := m.Commit([]harnesstypes.Write{
					session.InsertEntry(customEntry(rootID, nil, "root", nil)),
					session.InsertEntry(customEntry(childID, stringPointer(rootID), "child", nil)),
					session.InsertEntry(customEntry(siblingID, stringPointer(rootID), "sibling", nil)),
					session.SetValue(branch("main"), childID),
					session.SetValue(laneConfig("main"), configuration()),
					session.SetValue(laneState("main"), idleLaneState()),
					session.SetValue(branch("review"), siblingID),
					session.SetValue(laneConfig("review"), configuration()),
					session.SetValue(laneState("review"), idleLaneState()),
					session.SetValue(branch("notes"), rootID),
					session.SetValue(applicationValue, map[string]any{"copied": true}),
				}, ctx)
				return err
			}); err != nil {
				return err
			}
			fork, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "tree", ID: stringPointer("fork")})
			if err != nil {
				return err
			}
			entries, err := fork.FindEntries(&harnesstypes.EntryQuery{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{rootID, childID, siblingID}, entryIDs(entries), "entries"); err != nil {
				return err
			}
			if tip, _, err := getBranchTip(fork, "main"); err != nil || tip == nil || *tip != childID {
				return fmt.Errorf("unexpected main tip")
			}
			if tip, _, err := getBranchTip(fork, "review"); err != nil || tip == nil || *tip != siblingID {
				return fmt.Errorf("unexpected review tip")
			}
			if _, ok, err := fork.GetValue(laneConfig("notes"), ctx); err != nil || ok {
				return fmt.Errorf("data-only branch must stay data-only")
			}
			stored, ok, err := fork.GetValue(applicationValue, ctx)
			if err != nil || !ok {
				return fmt.Errorf("expected copied application value")
			}
			_ = stored
			_ = source.Close(ctx)
			return fork.Close(ctx)
		}),
		repoCase(factory, "forks", "rejects only surviving unknown reserved scalar state", func(repo SessionRepoBackend[T]) error {
			source, err := repo.Create("source", nil)
			if err != nil {
				return err
			}
			if _, err := source.CreateBranch("main", nil, ctx); err != nil {
				return err
			}
			if err := sessionMutate(source, func(m harnesstypes.SessionMutator) error {
				_, err := m.Commit([]harnesstypes.Write{
					session.SetValue(laneConfig("main"), configuration()),
					session.SetValue(laneState("main"), idleLaneState()),
				}, ctx)
				return err
			}); err != nil {
				return err
			}
			for _, namespace := range []string{"pi", "pi.unknown"} {
				address := session.NewValue(namespace)
				if err := source.SetValue(address, true, ctx); err != nil {
					return err
				}
				if _, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "tree", ID: stringPointer("tree")}); err == nil {
					return fmt.Errorf("expected tree fork to reject namespace %s", namespace)
				}
				if _, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("main"), ID: stringPointer("branch")}); err == nil {
					return fmt.Errorf("expected branch fork to reject namespace %s", namespace)
				}
				if err := source.DeleteValue(address, ctx); err != nil {
					return err
				}
			}
			_ = source.Close(ctx)
			tree, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "tree", ID: stringPointer("tree")})
			if err != nil {
				return err
			}
			branchFork, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("main"), ID: stringPointer("branch")})
			if err != nil {
				return err
			}
			_ = tree.Close(ctx)
			return branchFork.Close(ctx)
		}),
	}
}

// CreateSessionRepoForkDestinationReservationConformance creates destination
// reservation cases.
func CreateSessionRepoForkDestinationReservationConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	return []sessiontesting.ConformanceCase{
		repoCase(factory, "fork coordination", "publishes create when it reserves a shared destination id first", func(repo SessionRepoBackend[T]) error {
			source, err := repo.Create("source", nil)
			if err != nil {
				return err
			}
			created, createErr := repo.Create("destination", nil)
			_, forkErr := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "tree", ID: stringPointer("destination")})
			if createErr != nil || forkErr == nil {
				return fmt.Errorf("expected create to win the reservation")
			}
			if created != nil {
				_ = created.Close(ctx)
			}
			return source.Close(ctx)
		}),
		repoCase(factory, "fork coordination", "publishes fork when it reserves a shared destination id first", func(repo SessionRepoBackend[T]) error {
			source, err := repo.Create("source", nil)
			if err != nil {
				return err
			}
			fork, forkErr := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "tree", ID: stringPointer("destination")})
			_, createErr := repo.Create("destination", nil)
			if forkErr != nil || createErr == nil {
				return fmt.Errorf("expected fork to win the reservation")
			}
			if fork != nil {
				_ = fork.Close(ctx)
			}
			return source.Close(ctx)
		}),
	}
}

// CreateSessionRepoForkSourceSnapshotConformance creates the source-snapshot
// case.
func CreateSessionRepoForkSourceSnapshotConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	return []sessiontesting.ConformanceCase{
		repoCase(factory, "fork coordination", "captures one coherent boundary between source commits", func(repo SessionRepoBackend[T]) error {
			source, err := repo.Create("source", nil)
			if err != nil {
				return err
			}
			firstMutation, err := source.BeginMutation(ctx)
			if err != nil {
				return err
			}
			if _, err := firstMutation.Commit([]harnesstypes.Write{
				session.InsertEntry(customEntry(rootID, nil, "first", nil)),
				session.SetValue(branch("main"), rootID),
				session.SetValue(laneConfig("main"), configuration()),
				session.SetValue(laneState("main"), idleLaneState()),
				session.SetValue(session.SessionName, "first name"),
				session.SetValue(session.EntryLabel(rootID), "first label"),
			}, ctx); err != nil {
				return err
			}
			fork, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("main"), ID: stringPointer("fork")})
			if err != nil {
				return err
			}
			if err := firstMutation.End(ctx); err != nil {
				return err
			}
			tip, _, err := getBranchTip(fork, "main")
			if err != nil {
				return err
			}
			if tip == nil || *tip != rootID {
				return fmt.Errorf("unexpected tip")
			}
			name, err := fork.GetName(ctx)
			if err != nil || name == nil || *name != "first name" {
				return fmt.Errorf("unexpected name")
			}
			label, err := fork.GetLabel(rootID, ctx)
			if err != nil || label == nil || *label != "first label" {
				return fmt.Errorf("unexpected label")
			}
			_ = source.Close(ctx)
			return fork.Close(ctx)
		}),
	}
}

// CreateSessionRepoStreamingForkConformance creates the streaming fork cases.
func CreateSessionRepoStreamingForkConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	cases := []sessiontesting.ConformanceCase{}
	cases = append(cases, repoCase(factory, "fork lane validation", "ignores malformed unrelated lanes", func(repo SessionRepoBackend[T]) error {
		source, err := repo.Create("source", nil)
		if err != nil {
			return err
		}
		if _, err := source.CreateBranch("main", nil, ctx); err != nil {
			return err
		}
		if err := sessionMutate(source, func(m harnesstypes.SessionMutator) error {
			_, err := m.Commit([]harnesstypes.Write{
				session.SetValue(laneConfig("main"), configuration()),
				session.SetValue(laneState("main"), idleLaneState()),
				session.SetValue(laneConfig("unrelated"), configuration()),
			}, ctx)
			return err
		}); err != nil {
			return err
		}
		tree, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "tree", ID: stringPointer("tree")})
		if err != nil {
			return err
		}
		branchFork, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("main"), ID: stringPointer("branch")})
		if err != nil {
			return err
		}
		tip, _, err := getBranchTip(branchFork, "main")
		if err != nil || tip != nil {
			return fmt.Errorf("unexpected tip")
		}
		_ = source.Close(ctx)
		_ = tree.Close(ctx)
		return branchFork.Close(ctx)
	}))
	cases = append(cases, repoCase(factory, "fork application lists", "tree fork copies lists at distinct addresses", func(repo SessionRepoBackend[T]) error {
		source, err := repo.Create("source", nil)
		if err != nil {
			return err
		}
		events := session.NewList("test.application.events")
		sibling := session.NewList(events.Namespace, "other")
		otherNamespace := session.NewList("pi2.events")
		absent := session.NewList(events.Namespace, "absent")
		if err := source.AppendList(events, "event", ctx); err != nil {
			return err
		}
		if err := source.AppendList(sibling, "sibling", ctx); err != nil {
			return err
		}
		if err := source.AppendList(otherNamespace, "other namespace", ctx); err != nil {
			return err
		}
		if err := source.Close(ctx); err != nil {
			return err
		}
		fork, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "tree", ID: stringPointer("fork")})
		if err != nil {
			return err
		}
		check := func(address harnesstypes.ValueList, want []any) error {
			elements, err := fork.ReadList(address, nil, ctx)
			if err != nil {
				return err
			}
			values := make([]any, 0, len(elements))
			for _, element := range elements {
				values = append(values, element.Value)
			}
			return assertEqual(want, values, "list values")
		}
		if err := check(events, []any{"event"}); err != nil {
			return err
		}
		if err := check(sibling, []any{"sibling"}); err != nil {
			return err
		}
		if err := check(otherNamespace, []any{"other namespace"}); err != nil {
			return err
		}
		return check(absent, []any{})
	}))
	cases = append(cases, repoCase(factory, "fork application lists", "tree fork copies only survivors after list deletion and reappend", func(repo SessionRepoBackend[T]) error {
		source, err := repo.Create("source", nil)
		if err != nil {
			return err
		}
		events := session.NewList("test.application.events")
		deleted := session.NewList(events.Namespace, "deleted")
		if err := source.AppendList(events, "old", ctx); err != nil {
			return err
		}
		if err := source.AppendList(deleted, "removed", ctx); err != nil {
			return err
		}
		if err := sessionMutate(source, func(m harnesstypes.SessionMutator) error {
			_, err := m.Commit([]harnesstypes.Write{
				session.DeleteList(events),
				session.AppendList(events, "temporary"),
				session.DeleteList(events),
				session.AppendList(events, "first survivor"),
				session.DeleteList(deleted),
			}, ctx)
			return err
		}); err != nil {
			return err
		}
		if err := source.AppendList(events, "second survivor", ctx); err != nil {
			return err
		}
		fork, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "tree", ID: stringPointer("fork")})
		if err != nil {
			return err
		}
		elements, err := fork.ReadList(events, nil, ctx)
		if err != nil {
			return err
		}
		values := make([]any, 0, len(elements))
		for _, element := range elements {
			values = append(values, element.Value)
		}
		if err := assertEqual([]any{"first survivor", "second survivor"}, values, "list values"); err != nil {
			return err
		}
		elements, err = fork.ReadList(deleted, nil, ctx)
		if err != nil {
			return err
		}
		return assertEqual(0, len(elements), "deleted list")
	}))
	cases = append(cases, repoCase(factory, "branch fork application state", "excludes overwritten and unchanged application values", func(repo SessionRepoBackend[T]) error {
		source, err := repo.Create("source", nil)
		if err != nil {
			return err
		}
		state := session.NewValue("test.application.state")
		unchanged := session.NewValue("test.application.settings")
		br, err := source.CreateBranch("review", nil, ctx)
		if err != nil {
			return err
		}
		if err := sessionMutate(source, func(m harnesstypes.SessionMutator) error {
			_, err := m.Commit([]harnesstypes.Write{
				session.SetValue(laneConfig("review"), configuration()),
				session.SetValue(laneState("review"), idleLaneState()),
				session.SetValue(state, "v1"),
				session.SetValue(unchanged, "predates fork point"),
			}, ctx)
			return err
		}); err != nil {
			return err
		}
		entryID, err := br.AppendCustomEntry("fork-point", nil, ctx)
		if err != nil {
			return err
		}
		if err := source.SetValue(state, "v2", ctx); err != nil {
			return err
		}
		fork, err := repo.Fork(source.Metadata(), harnesstypes.ForkOptions{Scope: "branch", Branch: stringPointer("review"), EntryID: &entryID})
		if err != nil {
			return err
		}
		if _, ok, err := fork.GetValue(state, ctx); err != nil || ok {
			return fmt.Errorf("overwritten state should be excluded")
		}
		if _, ok, err := fork.GetValue(unchanged, ctx); err != nil || ok {
			return fmt.Errorf("unchanged state should be excluded")
		}
		_ = source.Close(ctx)
		return fork.Close(ctx)
	}))
	return cases
}

// CreateSessionRepoForkCoordinationConformance combines destination
// reservation and source snapshot cases.
func CreateSessionRepoForkCoordinationConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	cases := []sessiontesting.ConformanceCase{}
	cases = append(cases, CreateSessionRepoForkDestinationReservationConformance(factory)...)
	cases = append(cases, CreateSessionRepoForkSourceSnapshotConformance(factory)...)
	return cases
}

// CreateSessionRepoForkConformance combines every fork case.
func CreateSessionRepoForkConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	cases := []sessiontesting.ConformanceCase{}
	cases = append(cases, CreateSessionRepoForkBehaviorConformance(factory)...)
	cases = append(cases, CreateSessionRepoForkCoordinationConformance(factory)...)
	return cases
}

// CreateSessionRepoConformance combines every repository case.
func CreateSessionRepoConformance[T harnesstypes.SessionMetadataView](factory SessionRepoConformanceFactory[T]) []sessiontesting.ConformanceCase {
	cases := []sessiontesting.ConformanceCase{}
	cases = append(cases, CreateSessionRepoLifecycleConformance(factory)...)
	cases = append(cases, CreateSessionRepoOwnershipConformance(factory)...)
	cases = append(cases, CreateSessionRepoMessageConformance(factory)...)
	cases = append(cases, CreateSessionRepoForkConformance(factory)...)
	return cases
}
