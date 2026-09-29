// Package conformance holds the runner-independent session conformance cases.
package conformance

import (
	"fmt"
	"math"
	"reflect"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	"github.com/minifish-org/pith/packages/agent/harness/session"
	sessiontesting "github.com/minifish-org/pith/packages/agent/harness/session/testing"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	usageutils "github.com/minifish-org/pith/packages/agent/harness/utils/usage"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

const messageTimestamp = 1_650_000_000_000

var ctx = harnesscontext.BackgroundContext

func testValueAddress(key string) harnesstypes.Value { return session.NewValue("test.value", key) }
func testValuePrefix(prefix string) harnesstypes.Value {
	return session.NewValue("test.value", prefix)
}
func testListAddress(key string) harnesstypes.ValueList { return session.NewList("test.list", key) }

var testName = session.NewValue("test.session.name")

type usageOptions struct {
	cacheWrite1h *float64
	reasoning    *float64
}

func usage(input, output float64, options usageOptions) aitypes.Usage {
	cacheRead := input + 1
	cacheWrite := output + 1
	return aitypes.Usage{
		Input:        input,
		Output:       output,
		CacheRead:    cacheRead,
		CacheWrite:   cacheWrite,
		CacheWrite1h: options.cacheWrite1h,
		Reasoning:    options.reasoning,
		TotalTokens:  input + output,
		Cost: aitypes.UsageCost{
			Input:      input / 100,
			Output:     output / 100,
			CacheRead:  cacheRead / 100,
			CacheWrite: cacheWrite / 100,
			Total:      (input + output + 2) / 100,
		},
	}
}

func zeroUsage() aitypes.Usage { return usageutils.EmptyUsage() }

func floatPointer(value float64) *float64 { return &value }

func userEntry(id string, parentID *string, text string) harnesstypes.MessageEntry {
	message := aitypes.NewUserMessageVariant(aitypes.NewUserMessageBlocks([]aitypes.ContentBlock{aitypes.TextBlock(text)}, messageTimestamp))
	return harnesstypes.MessageEntry{
		EntryBase: harnesstypes.EntryBase{ID: id, ParentID: parentID, Type: harnesstypes.EntryTypeMessage},
		Message:   agenttypes.NewAgentMessageFromMessage(message),
	}
}

func customEntry(id string, parentID *string, customType string, data harnesstypes.JsonValue) harnesstypes.CustomEntry {
	return harnesstypes.CustomEntry{
		EntryBase:  harnesstypes.EntryBase{ID: id, ParentID: parentID, Type: harnesstypes.EntryTypeCustom},
		CustomType: customType,
		Data:       data,
	}
}

func compactionEntry(id string, parentID *string) harnesstypes.CompactionEntry {
	return harnesstypes.CompactionEntry{
		EntryBase:    harnesstypes.EntryBase{ID: id, ParentID: parentID, Type: harnesstypes.EntryTypeCompaction},
		Summary:      "summary:" + id,
		RetainedTail: []agenttypes.AgentMessage{},
		TokensBefore: 10,
	}
}

func entryIDs(entries []harnesstypes.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, session.EntryID(entry))
	}
	return out
}

func assertEqual(want, got any, label string) error {
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("%s: want %#v, got %#v", label, want, got)
	}
	return nil
}

func assertTrue(condition bool, label string) error {
	if !condition {
		return fmt.Errorf("%s", label)
	}
	return nil
}

func assertStrictlyIncreasing(values []int) error {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return fmt.Errorf("expected %v to be strictly increasing", values)
		}
	}
	return nil
}

func assertCommitStats(storage harnesstypes.Storage, result harnesstypes.CommitResult) error {
	stats, err := storage.GetStats(ctx)
	if err != nil {
		return err
	}
	return assertEqual(stats, result.Stats, "commit stats")
}

func createStorageCase(factory sessiontesting.StorageFixtureFunc, group, name string, test func(sessiontesting.StorageFixture) error) sessiontesting.ConformanceCase {
	return sessiontesting.ConformanceCase{Group: group, Name: name, Run: func() error {
		fixture, err := factory()
		if err != nil {
			return err
		}
		if fixture.Close != nil {
			defer fixture.Close()
		}
		return test(fixture)
	}}
}

// CreateStorageConformance creates fresh cases for the durable Storage
// contract.
func CreateStorageConformance(factory sessiontesting.StorageFixtureFunc) []sessiontesting.ConformanceCase {
	return []sessiontesting.ConformanceCase{
		createStorageCase(factory, "transactions", "commits mixed writes atomically in write order", func(f sessiontesting.StorageFixture) error {
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("entry", nil, "entry")),
				session.SetValue(testName, "session"),
				session.InsertUsage(harnesstypes.UsageRow{ID: "usage", Usage: usage(2, 3, usageOptions{}), EntryID: stringPointer("entry")}),
			}, ctx)
			if err != nil {
				return err
			}
			if len(result.Seqs) != 3 || result.FirstSeq != result.Seqs[0] {
				return fmt.Errorf("unexpected sequences %v", result.Seqs)
			}
			if err := assertCommitStats(f.Storage, result); err != nil {
				return err
			}
			if err := assertStrictlyIncreasing(result.Seqs); err != nil {
				return err
			}
			entries, err := f.Storage.GetEntries([]string{"entry"}, ctx)
			if err != nil {
				return err
			}
			wantEntry := userEntry("entry", nil, "entry")
			wantEntry.Seq = result.Seqs[0]
			wantEntry.Timestamp = result.Timestamp
			if err := assertEqual(map[string]harnesstypes.Entry{"entry": wantEntry}, entries, "entries"); err != nil {
				return err
			}
			stored, ok, err := f.Storage.GetValue(testName, ctx)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("missing stored value")
			}
			if err := assertEqual(harnesstypes.StoredValue{Address: testName, Value: "session", Seq: result.Seqs[1]}, stored, "stored value"); err != nil {
				return err
			}
			rows, err := f.Storage.ScanUsage(harnesstypes.UsageScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			wantRows := []harnesstypes.UsageRow{{ID: "usage", Seq: result.Seqs[2], Usage: usage(2, 3, usageOptions{}), EntryID: stringPointer("entry")}}
			return assertEqual(wantRows, rows, "usage rows")
		}),
		createStorageCase(factory, "transactions", "rolls back every store when a mixed transaction fails", func(f sessiontesting.StorageFixture) error {
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("root", nil, "root")),
				session.InsertUsage(harnesstypes.UsageRow{ID: "taken", Usage: usage(1, 1, usageOptions{})}),
			}, ctx); err != nil {
				return err
			}
			entriesBefore, err := f.Storage.ScanEntries(harnesstypes.EntryScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			usageBefore, err := f.Storage.ScanUsage(harnesstypes.UsageScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			statsBefore, err := f.Storage.GetStats(ctx)
			if err != nil {
				return err
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.SetValue(testName, "transient"),
				session.InsertEntry(customEntry("transient-entry", stringPointer("root"), "note", map[string]any{"id": "transient-entry"})),
				session.InsertUsage(harnesstypes.UsageRow{ID: "transient-usage", Usage: usage(5, 8, usageOptions{}), Adjustment: true}),
				session.InsertEntry(customEntry("taken", stringPointer("root"), "note", map[string]any{"id": "taken"})),
			}, ctx); err == nil {
				return fmt.Errorf("expected the mixed transaction to fail")
			}
			entriesAfter, err := f.Storage.ScanEntries(harnesstypes.EntryScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(entriesBefore, entriesAfter, "entries after rollback"); err != nil {
				return err
			}
			usageAfter, err := f.Storage.ScanUsage(harnesstypes.UsageScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(usageBefore, usageAfter, "usage after rollback"); err != nil {
				return err
			}
			statsAfter, err := f.Storage.GetStats(ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(statsBefore, statsAfter, "stats after rollback"); err != nil {
				return err
			}
			if _, ok, err := f.Storage.GetValue(testName, ctx); err != nil || ok {
				return fmt.Errorf("transient value survived rollback: %v", err)
			}
			return nil
		}),
		createStorageCase(factory, "transactions", "preserves overwritten and deleted values when a transaction fails", func(f sessiontesting.StorageFixture) error {
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.SetValue(testValueAddress("overwritten"), "original"),
				session.SetValue(testValueAddress("deleted"), map[string]any{"kept": true}),
				session.InsertEntry(userEntry("taken", nil, "taken")),
			}, ctx); err != nil {
				return err
			}
			overwrittenBefore, _, err := f.Storage.GetValue(testValueAddress("overwritten"), ctx)
			if err != nil {
				return err
			}
			deletedBefore, _, err := f.Storage.GetValue(testValueAddress("deleted"), ctx)
			if err != nil {
				return err
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.SetValue(testValueAddress("overwritten"), "transient"),
				session.DeleteValue(testValueAddress("deleted")),
				session.InsertEntry(customEntry("transient", stringPointer("taken"), "note", map[string]any{"id": "transient"})),
				session.InsertEntry(customEntry("taken", nil, "note", map[string]any{"id": "taken"})),
			}, ctx); err == nil {
				return fmt.Errorf("expected the transaction to fail")
			}
			overwrittenAfter, _, err := f.Storage.GetValue(testValueAddress("overwritten"), ctx)
			if err != nil {
				return err
			}
			deletedAfter, _, err := f.Storage.GetValue(testValueAddress("deleted"), ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(overwrittenBefore, overwrittenAfter, "overwritten value"); err != nil {
				return err
			}
			if err := assertEqual(deletedBefore, deletedAfter, "deleted value"); err != nil {
				return err
			}
			entries, err := f.Storage.GetEntries([]string{"transient"}, ctx)
			if err != nil {
				return err
			}
			if _, ok := entries["transient"]; ok {
				return fmt.Errorf("transient entry survived rollback")
			}
			return nil
		}),
		createStorageCase(factory, "transactions", "enforces one shared entry and usage id namespace", func(f sessiontesting.StorageFixture) error {
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("existing-entry", nil, "existing-entry")),
				session.InsertUsage(harnesstypes.UsageRow{ID: "existing-usage", Usage: usage(1, 1, usageOptions{})}),
			}, ctx); err != nil {
				return err
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{session.InsertUsage(harnesstypes.UsageRow{ID: "existing-entry", Usage: usage(2, 2, usageOptions{})})}, ctx); err == nil {
				return fmt.Errorf("expected duplicate usage id to reject")
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{session.InsertEntry(customEntry("existing-usage", nil, "note", nil))}, ctx); err == nil {
				return fmt.Errorf("expected duplicate entry id to reject")
			}
			for _, id := range []string{"entry-then-usage", "usage-then-entry"} {
				var writes []harnesstypes.Write
				if id == "entry-then-usage" {
					writes = []harnesstypes.Write{
						session.InsertEntry(customEntry(id, nil, "note", nil)),
						session.InsertUsage(harnesstypes.UsageRow{ID: id, Usage: usage(3, 3, usageOptions{})}),
					}
				} else {
					writes = []harnesstypes.Write{
						session.InsertUsage(harnesstypes.UsageRow{ID: id, Usage: usage(4, 4, usageOptions{})}),
						session.InsertEntry(customEntry(id, nil, "note", nil)),
					}
				}
				if _, err := f.Storage.Commit(writes, ctx); err == nil {
					return fmt.Errorf("expected duplicate id %s to reject", id)
				}
			}
			entries, err := f.Storage.ScanEntries(harnesstypes.EntryScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"existing-entry"}, entryIDs(entries), "entries"); err != nil {
				return err
			}
			rows, err := f.Storage.ScanUsage(harnesstypes.UsageScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			rowIDs := make([]string, 0, len(rows))
			for _, row := range rows {
				rowIDs = append(rowIDs, row.ID)
			}
			return assertEqual([]string{"existing-usage"}, rowIDs, "usage ids")
		}),
		createStorageCase(factory, "transactions", "resolves parents only from prior entries and earlier writes", func(f sessiontesting.StorageFixture) error {
			if _, err := f.Storage.Commit([]harnesstypes.Write{session.InsertEntry(userEntry("root", nil, "root"))}, ctx); err != nil {
				return err
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(customEntry("child", stringPointer("root"), "note", map[string]any{"id": "child"})),
				session.InsertEntry(customEntry("grandchild", stringPointer("child"), "note", map[string]any{"id": "grandchild"})),
			}, ctx); err != nil {
				return err
			}
			branch, err := f.Storage.ScanBranch(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{Order: orderPointer("oldestFirst")}, Start: "grandchild"}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"root", "child", "grandchild"}, entryIDs(branch), "branch"); err != nil {
				return err
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(customEntry("before-parent", stringPointer("later-parent"), "note", map[string]any{"id": "before-parent"})),
				session.InsertEntry(customEntry("later-parent", stringPointer("root"), "note", map[string]any{"id": "later-parent"})),
				session.SetValue(session.EntryLabel("before-parent"), "transient"),
			}, ctx); err == nil {
				return fmt.Errorf("expected forward parent to reject")
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{session.InsertEntry(customEntry("orphan", stringPointer("missing"), "note", map[string]any{"id": "orphan"}))}, ctx); err == nil {
				return fmt.Errorf("expected missing parent to reject")
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{session.InsertUsage(harnesstypes.UsageRow{ID: "usage-is-not-parent", Usage: usage(1, 1, usageOptions{})})}, ctx); err != nil {
				return err
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{session.InsertEntry(customEntry("usage-child", stringPointer("usage-is-not-parent"), "note", map[string]any{"id": "usage-child"}))}, ctx); err == nil {
				return fmt.Errorf("expected usage parent to reject")
			}
			entries, err := f.Storage.GetEntries([]string{"before-parent", "later-parent", "orphan", "usage-child"}, ctx)
			if err != nil {
				return err
			}
			if len(entries) != 0 {
				return fmt.Errorf("transient entries survived: %v", entries)
			}
			if _, ok, err := f.Storage.GetValue(session.EntryLabel("before-parent"), ctx); err != nil || ok {
				return fmt.Errorf("transient label survived")
			}
			return nil
		}),
		createStorageCase(factory, "transactions", "places pending content under its reserved entry id", func(f sessiontesting.StorageFixture) error {
			entry := userEntry("reserved", nil, "queued")
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.SetValue(session.PendingEntry(entry.ID), map[string]any{"type": "message", "payload": entry.Message}),
				session.SetValue(session.BranchTip("main"), nil),
			}, ctx); err != nil {
				return err
			}
			entries, err := f.Storage.GetEntries([]string{entry.ID}, ctx)
			if err != nil {
				return err
			}
			if _, ok := entries[entry.ID]; ok {
				return fmt.Errorf("reserved entry must not exist yet")
			}
			pending, ok, err := f.Storage.GetValue(session.PendingEntry(entry.ID), ctx)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("missing pending entry")
			}
			_ = pending
			tip, ok, err := f.Storage.GetValue(session.BranchTip("main"), ctx)
			if err != nil || !ok || tip.Value != nil {
				return fmt.Errorf("branch tip should be null: %v %v", tip, err)
			}
			placement, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(entry),
				session.DeleteValue(session.PendingEntry(entry.ID)),
				session.SetValue(session.BranchTip("main"), entry.ID),
			}, ctx)
			if err != nil {
				return err
			}
			entries, err = f.Storage.GetEntries([]string{entry.ID}, ctx)
			if err != nil {
				return err
			}
			wantEntry := entry
			wantEntry.Seq = placement.Seqs[0]
			wantEntry.Timestamp = placement.Timestamp
			if err := assertEqual(map[string]harnesstypes.Entry{entry.ID: wantEntry}, entries, "placed entry"); err != nil {
				return err
			}
			if _, ok, err := f.Storage.GetValue(session.PendingEntry(entry.ID), ctx); err != nil || ok {
				return fmt.Errorf("pending entry should be deleted")
			}
			tip, ok, err = f.Storage.GetValue(session.BranchTip("main"), ctx)
			if err != nil || !ok {
				return fmt.Errorf("missing branch tip")
			}
			if err := assertEqual(harnesstypes.StoredValue{Address: session.BranchTip("main"), Value: entry.ID, Seq: placement.Seqs[2]}, tip, "branch tip"); err != nil {
				return err
			}
			return nil
		}),
		createStorageCase(factory, "values", "sets, replaces, deletes, and recreates values without tombstones", func(f sessiontesting.StorageFixture) error {
			first, err := f.Storage.Commit([]harnesstypes.Write{
				session.SetValue(testValueAddress("prefix/b"), float64(1)),
				session.SetValue(testValueAddress("prefix/a"), float64(2)),
				session.SetValue(testValueAddress("other"), float64(3)),
				session.SetValue(testValueAddress("prefix/\ue000"), float64(4)),
				session.SetValue(testValueAddress("prefix/\U00010000"), float64(5)),
				session.SetValue(testValueAddress("prefix/a"), nil),
			}, ctx)
			if err != nil {
				return err
			}
			stored, _, err := f.Storage.GetValue(testValueAddress("prefix/a"), ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(harnesstypes.StoredValue{Address: testValueAddress("prefix/a"), Value: nil, Seq: first.Seqs[5]}, stored, "prefix/a"); err != nil {
				return err
			}
			second, err := f.Storage.Commit([]harnesstypes.Write{
				session.DeleteValue(testValueAddress("prefix/a")),
				session.DeleteValue(testValueAddress("absent")),
				session.SetValue(testValueAddress("prefix/a"), "recreated"),
			}, ctx)
			if err != nil {
				return err
			}
			values, err := f.Storage.ScanValues(testValuePrefix("prefix/"), ctx)
			if err != nil {
				return err
			}
			want := []harnesstypes.StoredValue{
				{Address: testValueAddress("prefix/a"), Value: "recreated", Seq: second.Seqs[2]},
				{Address: testValueAddress("prefix/b"), Value: float64(1), Seq: first.Seqs[0]},
				{Address: testValueAddress("prefix/\ue000"), Value: float64(4), Seq: first.Seqs[3]},
				{Address: testValueAddress("prefix/\U00010000"), Value: float64(5), Seq: first.Seqs[4]},
			}
			if err := assertEqual(want, values, "scanned values"); err != nil {
				return err
			}
			if _, ok, err := f.Storage.GetValue(testValueAddress("absent"), ctx); err != nil || ok {
				return fmt.Errorf("absent value should not exist")
			}
			return nil
		}),
		createStorageCase(factory, "values", "applies same-transaction value and list operations in write order", func(f sessiontesting.StorageFixture) error {
			keptValue := testValueAddress("write-order/kept")
			deletedValue := testValueAddress("write-order/deleted")
			keptList := testListAddress("write-order/kept")
			deletedList := testListAddress("write-order/deleted")
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.SetValue(deletedValue, "transient"),
				session.DeleteValue(deletedValue),
				session.SetValue(keptValue, "transient"),
				session.SetValue(keptValue, "kept"),
				session.AppendList(keptList, "transient"),
				session.DeleteList(keptList),
				session.AppendList(keptList, "kept"),
				session.AppendList(deletedList, "transient"),
				session.DeleteList(deletedList),
			}, ctx)
			if err != nil {
				return err
			}
			if _, ok, err := f.Storage.GetValue(deletedValue, ctx); err != nil || ok {
				return fmt.Errorf("deleted value should not exist")
			}
			stored, _, err := f.Storage.GetValue(keptValue, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(harnesstypes.StoredValue{Address: keptValue, Value: "kept", Seq: result.Seqs[3]}, stored, "kept value"); err != nil {
				return err
			}
			elements, err := f.Storage.ReadList(keptList, nil, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]harnesstypes.ListElement{{Seq: result.Seqs[6], Value: "kept"}}, elements, "kept list"); err != nil {
				return err
			}
			elements, err = f.Storage.ReadList(deletedList, nil, ctx)
			if err != nil {
				return err
			}
			return assertEqual([]harnesstypes.ListElement{}, elements, "deleted list")
		}),
		createStorageCase(factory, "values", "does not change historical stores during value-only commits", func(f sessiontesting.StorageFixture) error {
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("root", nil, "root")),
				session.InsertUsage(harnesstypes.UsageRow{ID: "historical-usage", Usage: usage(2, 3, usageOptions{})}),
			}, ctx); err != nil {
				return err
			}
			entriesBefore, err := f.Storage.ScanEntries(harnesstypes.EntryScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			usageBefore, err := f.Storage.ScanUsage(harnesstypes.UsageScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			statsBefore, err := f.Storage.GetStats(ctx)
			if err != nil {
				return err
			}
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.SetValue(testName, "first"),
				session.SetValue(testName, "second"),
			}, ctx)
			if err != nil {
				return err
			}
			entriesAfter, err := f.Storage.ScanEntries(harnesstypes.EntryScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(entriesBefore, entriesAfter, "entries"); err != nil {
				return err
			}
			usageAfter, err := f.Storage.ScanUsage(harnesstypes.UsageScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(usageBefore, usageAfter, "usage"); err != nil {
				return err
			}
			statsAfter, err := f.Storage.GetStats(ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(statsBefore, statsAfter, "stats"); err != nil {
				return err
			}
			stored, _, err := f.Storage.GetValue(testName, ctx)
			if err != nil {
				return err
			}
			return assertEqual(harnesstypes.StoredValue{Address: testName, Value: "second", Seq: result.Seqs[1]}, stored, "value")
		}),
		createStorageCase(factory, "lists", "pages appends by global sequence and deletes whole lists", func(f sessiontesting.StorageFixture) error {
			address := testListAddress("events")
			elements, err := f.Storage.ReadList(address, nil, ctx)
			if err != nil {
				return err
			}
			if len(elements) != 0 {
				return fmt.Errorf("expected empty list")
			}
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.AppendList(address, "a"),
				session.SetValue(testName, "gap"),
				session.AppendList(address, "b"),
				session.AppendList(address, "c"),
			}, ctx)
			if err != nil {
				return err
			}
			elements, err = f.Storage.ReadList(address, nil, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]harnesstypes.ListElement{
				{Seq: result.Seqs[0], Value: "a"},
				{Seq: result.Seqs[2], Value: "b"},
				{Seq: result.Seqs[3], Value: "c"},
			}, elements, "full list"); err != nil {
				return err
			}
			limit := 2
			elements, err = f.Storage.ReadList(address, &harnesstypes.ListReadOptions{Limit: &limit}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]harnesstypes.ListElement{
				{Seq: result.Seqs[0], Value: "a"},
				{Seq: result.Seqs[2], Value: "b"},
			}, elements, "limited list"); err != nil {
				return err
			}
			elements, err = f.Storage.ReadList(address, &harnesstypes.ListReadOptions{Cursor: &harnesstypes.ListCursor{Seq: result.Seqs[0]}, Limit: &limit}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]harnesstypes.ListElement{
				{Seq: result.Seqs[2], Value: "b"},
				{Seq: result.Seqs[3], Value: "c"},
			}, elements, "cursor list"); err != nil {
				return err
			}
			desc := "desc"
			elements, err = f.Storage.ReadList(address, &harnesstypes.ListReadOptions{Order: &desc, Limit: &limit}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]harnesstypes.ListElement{
				{Seq: result.Seqs[3], Value: "c"},
				{Seq: result.Seqs[2], Value: "b"},
			}, elements, "desc list"); err != nil {
				return err
			}
			elements, err = f.Storage.ReadList(address, &harnesstypes.ListReadOptions{Order: &desc, Cursor: &harnesstypes.ListCursor{Seq: result.Seqs[3]}, Limit: &limit}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]harnesstypes.ListElement{
				{Seq: result.Seqs[2], Value: "b"},
				{Seq: result.Seqs[0], Value: "a"},
			}, elements, "desc cursor list"); err != nil {
				return err
			}
			zero := 0
			if _, err := f.Storage.ReadList(address, &harnesstypes.ListReadOptions{Limit: &zero}, ctx); err == nil {
				return fmt.Errorf("expected zero limit to reject")
			}
			tooLarge := math.MaxInt
			if _, err := f.Storage.ReadList(address, &harnesstypes.ListReadOptions{Limit: &tooLarge}, ctx); err == nil {
				return fmt.Errorf("expected huge limit to reject")
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.DeleteList(address),
				session.DeleteList(testListAddress("absent")),
				session.AppendList(address, "new"),
			}, ctx); err != nil {
				return err
			}
			elements, err = f.Storage.ReadList(address, nil, ctx)
			if err != nil {
				return err
			}
			values := make([]any, 0, len(elements))
			for _, element := range elements {
				values = append(values, element.Value)
			}
			return assertEqual([]any{"new"}, values, "recreated list")
		}),
		createStorageCase(factory, "lists", "clamps one read page without limiting list growth", func(f sessiontesting.StorageFixture) error {
			address := testListAddress("large")
			writes := make([]harnesstypes.Write, 0, 10001)
			for index := 0; index < 10001; index++ {
				writes = append(writes, session.AppendList(address, float64(index)))
			}
			if _, err := f.Storage.Commit(writes, ctx); err != nil {
				return err
			}
			firstPage, err := f.Storage.ReadList(address, nil, ctx)
			if err != nil {
				return err
			}
			if len(firstPage) != 1000 {
				return fmt.Errorf("expected 1000, got %d", len(firstPage))
			}
			limit := 20000
			page, err := f.Storage.ReadList(address, &harnesstypes.ListReadOptions{Limit: &limit}, ctx)
			if err != nil {
				return err
			}
			if len(page) != 10000 {
				return fmt.Errorf("expected 10000, got %d", len(page))
			}
			page, err = f.Storage.ReadList(address, &harnesstypes.ListReadOptions{Cursor: &harnesstypes.ListCursor{Seq: firstPage[len(firstPage)-1].Seq}}, ctx)
			if err != nil {
				return err
			}
			return assertEqual(1000, len(page), "second page length")
		}),
		createStorageCase(factory, "lists", "commits mixed list writes atomically and rolls them back with siblings", func(f sessiontesting.StorageFixture) error {
			address := testListAddress("atomic")
			committed, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("mixed", nil, "mixed")),
				session.AppendList(address, "kept"),
				session.SetValue(testName, "kept"),
				session.InsertUsage(harnesstypes.UsageRow{ID: "mixed-usage", Usage: usage(1, 2, usageOptions{})}),
			}, ctx)
			if err != nil {
				return err
			}
			elements, err := f.Storage.ReadList(address, nil, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]harnesstypes.ListElement{{Seq: committed.Seqs[1], Value: "kept"}}, elements, "list"); err != nil {
				return err
			}
			if _, err := f.Storage.Commit([]harnesstypes.Write{
				session.AppendList(address, "transient"),
				session.DeleteValue(testName),
				session.InsertEntry(userEntry("mixed", nil, "mixed")),
			}, ctx); err == nil {
				return fmt.Errorf("expected rollback")
			}
			elements, err = f.Storage.ReadList(address, nil, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]harnesstypes.ListElement{{Seq: committed.Seqs[1], Value: "kept"}}, elements, "list after rollback"); err != nil {
				return err
			}
			stored, _, err := f.Storage.GetValue(testName, ctx)
			if err != nil {
				return err
			}
			return assertEqual(harnesstypes.StoredValue{Address: testName, Value: "kept", Seq: committed.Seqs[2]}, stored, "value")
		}),
		createStorageCase(factory, "entry queries", "stores custom entries with and without data", func(f sessiontesting.StorageFixture) error {
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(customEntry("without-data", nil, "marker", nil)),
				session.InsertEntry(customEntry("with-data", stringPointer("without-data"), "note", map[string]any{"nested": []any{float64(1), float64(2)}})),
			}, ctx)
			if err != nil {
				return err
			}
			entries, err := f.Storage.GetEntries([]string{"without-data", "with-data"}, ctx)
			if err != nil {
				return err
			}
			without := customEntry("without-data", nil, "marker", nil)
			without.Seq = result.Seqs[0]
			without.Timestamp = result.Timestamp
			with := customEntry("with-data", stringPointer("without-data"), "note", map[string]any{"nested": []any{float64(1), float64(2)}})
			with.Seq = result.Seqs[1]
			with.Timestamp = result.Timestamp
			return assertEqual(map[string]harnesstypes.Entry{"without-data": without, "with-data": with}, entries, "entries")
		}),
		createStorageCase(factory, "entry queries", "scans global entries with explicit ranges, filters, orders, and limits", func(f sessiontesting.StorageFixture) error {
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("root", nil, "root")),
				session.InsertEntry(customEntry("note-1", stringPointer("root"), "note", nil)),
				session.InsertEntry(customEntry("other", stringPointer("note-1"), "other", nil)),
				session.InsertEntry(customEntry("note-2", stringPointer("other"), "note", nil)),
				session.InsertEntry(userEntry("tail", stringPointer("note-2"), "tail")),
			}, ctx)
			if err != nil {
				return err
			}
			customType := "note"
			entries, err := f.Storage.ScanEntries(harnesstypes.EntryScan{
				Type:       entryTypePointer(harnesstypes.EntryTypeCustom),
				CustomType: &customType,
				FromSeq:    &result.Seqs[1],
				ToSeq:      &result.Seqs[3],
				Order:      orderPointer("desc"),
			}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"note-2", "note-1"}, entryIDs(entries), "filtered"); err != nil {
				return err
			}
			ascLimit := 2
			entries, err = f.Storage.ScanEntries(harnesstypes.EntryScan{Order: orderPointer("asc"), Limit: &ascLimit}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"root", "note-1"}, entryIDs(entries), "asc"); err != nil {
				return err
			}
			entries, err = f.Storage.ScanEntries(harnesstypes.EntryScan{Order: orderPointer("desc"), Limit: &ascLimit}, ctx)
			if err != nil {
				return err
			}
			return assertEqual([]string{"tail", "note-2"}, entryIDs(entries), "desc")
		}),
		createStorageCase(factory, "branch queries", "applies stops before filters and cursors before limits", func(f sessiontesting.StorageFixture) error {
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("root", nil, "root")),
				session.InsertEntry(customEntry("marker", stringPointer("root"), "marker", nil)),
				session.InsertEntry(userEntry("middle", stringPointer("marker"), "middle")),
				session.InsertEntry(compactionEntry("compact", stringPointer("middle"))),
				session.InsertEntry(customEntry("note", stringPointer("compact"), "note", nil)),
				session.InsertEntry(userEntry("leaf", stringPointer("note"), "leaf")),
			}, ctx)
			if err != nil {
				return err
			}
			entries, err := f.Storage.ScanBranch(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{StopAtType: entryTypePointer(harnesstypes.EntryTypeCompaction), Type: entryTypePointer(harnesstypes.EntryTypeMessage)}, Start: "leaf"}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"leaf"}, entryIDs(entries), "stop before filter"); err != nil {
				return err
			}
			orderAsc := "oldestFirst"
			entries, err = f.Storage.ScanBranch(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{Order: &orderAsc, StopAtID: stringPointer("middle"), Type: entryTypePointer(harnesstypes.EntryTypeCustom)}, Start: "leaf"}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"marker"}, entryIDs(entries), "stop id"); err != nil {
				return err
			}
			orderDesc := "newestFirst"
			entries, err = f.Storage.ScanBranch(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{Order: &orderDesc, Cursor: &harnesstypes.EntryCursor{Seq: result.Seqs[4]}, Limit: intPointer(2)}, Start: "leaf"}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"compact", "middle"}, entryIDs(entries), "newest cursor"); err != nil {
				return err
			}
			entries, err = f.Storage.ScanBranch(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{Order: &orderAsc, Cursor: &harnesstypes.EntryCursor{Seq: result.Seqs[1]}, Limit: intPointer(2)}, Start: "leaf"}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"middle", "compact"}, entryIDs(entries), "oldest cursor"); err != nil {
				return err
			}
			entries, err = f.Storage.ScanBranch(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{StopAtID: stringPointer("leaf"), Type: entryTypePointer(harnesstypes.EntryTypeCustom)}, Start: "leaf"}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{}, entryIDs(entries), "stop at leaf"); err != nil {
				return err
			}
			customType := "note"
			entries, err = f.Storage.ScanBranch(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{CustomType: &customType}, Start: "leaf"}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"note"}, entryIDs(entries), "custom type"); err != nil {
				return err
			}
			if _, err := f.Storage.ScanBranch(harnesstypes.StorageBranchScan{Start: "missing"}, ctx); err == nil {
				return fmt.Errorf("expected missing start to reject")
			}
			return nil
		}),
		createStorageCase(factory, "branch queries", "returns branch structure without payload fields", func(f sessiontesting.StorageFixture) error {
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("root", nil, "root")),
				session.InsertEntry(customEntry("child", stringPointer("root"), "note", nil)),
			}, ctx)
			if err != nil {
				return err
			}
			orderAsc := "oldestFirst"
			structure, err := f.Storage.ScanBranchStructure(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{Order: &orderAsc}, Start: "child"}, ctx)
			if err != nil {
				return err
			}
			customType := "note"
			want := []harnesstypes.EntryStructure{
				{ID: "root", ParentID: nil, Seq: result.Seqs[0], Timestamp: result.Timestamp, Type: harnesstypes.EntryTypeMessage},
				{ID: "child", ParentID: stringPointer("root"), Seq: result.Seqs[1], Timestamp: result.Timestamp, Type: harnesstypes.EntryTypeCustom, CustomType: &customType},
			}
			return assertEqual(want, structure, "structure")
		}),
		createStorageCase(factory, "branch queries", "applies branch query semantics to structure scans", func(f sessiontesting.StorageFixture) error {
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("root", nil, "root")),
				session.InsertEntry(customEntry("marker", stringPointer("root"), "marker", nil)),
				session.InsertEntry(userEntry("middle", stringPointer("marker"), "middle")),
				session.InsertEntry(compactionEntry("compact", stringPointer("middle"))),
				session.InsertEntry(customEntry("note", stringPointer("compact"), "note", nil)),
				session.InsertEntry(userEntry("leaf", stringPointer("note"), "leaf")),
			}, ctx)
			if err != nil {
				return err
			}
			structure, err := f.Storage.ScanBranchStructure(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{StopAtType: entryTypePointer(harnesstypes.EntryTypeCompaction), Type: entryTypePointer(harnesstypes.EntryTypeMessage)}, Start: "leaf"}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"leaf"}, structureIDs(structure), "structure stop"); err != nil {
				return err
			}
			orderAsc := "oldestFirst"
			structure, err = f.Storage.ScanBranchStructure(harnesstypes.StorageBranchScan{BranchScan: harnesstypes.BranchScan{Order: &orderAsc, Cursor: &harnesstypes.EntryCursor{Seq: result.Seqs[1]}, Limit: intPointer(2)}, Start: "leaf"}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"middle", "compact"}, structureIDs(structure), "structure cursor"); err != nil {
				return err
			}
			if _, err := f.Storage.ScanBranchStructure(harnesstypes.StorageBranchScan{Start: "missing"}, ctx); err == nil {
				return fmt.Errorf("expected missing start to reject")
			}
			return nil
		}),
		createStorageCase(factory, "usage and stats", "scans the usage ledger with explicit ranges, orders, and limits", func(f sessiontesting.StorageFixture) error {
			result, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertUsage(harnesstypes.UsageRow{ID: "usage-1", Usage: usage(1, 1, usageOptions{})}),
				session.SetValue(testName, "sequence gap"),
				session.InsertUsage(harnesstypes.UsageRow{ID: "usage-2", Usage: usage(2, 2, usageOptions{})}),
				session.InsertUsage(harnesstypes.UsageRow{ID: "usage-3", Usage: usage(3, 3, usageOptions{}), Adjustment: true}),
			}, ctx)
			if err != nil {
				return err
			}
			rows, err := f.Storage.ScanUsage(harnesstypes.UsageScan{FromSeq: &result.Seqs[1], ToSeq: &result.Seqs[2], Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"usage-2"}, rowIDs(rows), "range"); err != nil {
				return err
			}
			limit := 2
			rows, err = f.Storage.ScanUsage(harnesstypes.UsageScan{Order: orderPointer("desc"), Limit: &limit}, ctx)
			if err != nil {
				return err
			}
			if err := assertEqual([]string{"usage-3", "usage-2"}, rowIDs(rows), "desc"); err != nil {
				return err
			}
			rows, err = f.Storage.ScanUsage(harnesstypes.UsageScan{Order: orderPointer("asc"), Limit: &limit}, ctx)
			if err != nil {
				return err
			}
			return assertEqual([]string{"usage-1", "usage-2"}, rowIDs(rows), "asc")
		}),
		createStorageCase(factory, "usage and stats", "keeps stats equal to message count and ledger totals", func(f sessiontesting.StorageFixture) error {
			stats, err := f.Storage.GetStats(ctx)
			if err != nil {
				return err
			}
			if err := assertEqual(harnesstypes.SessionStats{MessageCount: 0, Usage: zeroUsage()}, stats, "empty stats"); err != nil {
				return err
			}
			firstUsage := usage(2, 3, usageOptions{cacheWrite1h: floatPointer(4), reasoning: floatPointer(1)})
			first, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(userEntry("message", nil, "message")),
				session.InsertUsage(harnesstypes.UsageRow{ID: "usage-1", Usage: firstUsage}),
			}, ctx)
			if err != nil {
				return err
			}
			if err := assertCommitStats(f.Storage, first); err != nil {
				return err
			}
			if err := assertEqual(harnesstypes.SessionStats{MessageCount: 1, Usage: firstUsage}, first.Stats, "first stats"); err != nil {
				return err
			}
			secondUsage := usage(5, 7, usageOptions{cacheWrite1h: floatPointer(6), reasoning: floatPointer(2)})
			second, err := f.Storage.Commit([]harnesstypes.Write{
				session.InsertEntry(customEntry("custom", stringPointer("message"), "note", nil)),
				session.InsertEntry(compactionEntry("compaction", stringPointer("custom"))),
				session.InsertUsage(harnesstypes.UsageRow{ID: "usage-2", Usage: secondUsage, Adjustment: true}),
			}, ctx)
			if err != nil {
				return err
			}
			if err := assertCommitStats(f.Storage, second); err != nil {
				return err
			}
			want := harnesstypes.SessionStats{
				MessageCount: 1,
				Usage: aitypes.Usage{
					Input:        7,
					Output:       10,
					CacheRead:    9,
					CacheWrite:   12,
					CacheWrite1h: floatPointer(10),
					Reasoning:    floatPointer(3),
					TotalTokens:  17,
					Cost: aitypes.UsageCost{
						Input:      firstUsage.Cost.Input + secondUsage.Cost.Input,
						Output:     firstUsage.Cost.Output + secondUsage.Cost.Output,
						CacheRead:  firstUsage.Cost.CacheRead + secondUsage.Cost.CacheRead,
						CacheWrite: firstUsage.Cost.CacheWrite + secondUsage.Cost.CacheWrite,
						Total:      firstUsage.Cost.Total + secondUsage.Cost.Total,
					},
				},
			}
			return assertEqual(want, second.Stats, "second stats")
		}),
		createStorageCase(factory, "serialization", "serializes back-to-back commits in admission order", func(f sessiontesting.StorageFixture) error {
			firstResult, err := f.Storage.Commit([]harnesstypes.Write{session.InsertEntry(userEntry("first", nil, "first"))}, ctx)
			if err != nil {
				return err
			}
			secondResult, err := f.Storage.Commit([]harnesstypes.Write{session.InsertEntry(userEntry("second", stringPointer("first"), "second"))}, ctx)
			if err != nil {
				return err
			}
			if firstResult.Seqs[0] >= secondResult.Seqs[0] {
				return fmt.Errorf("expected admission order")
			}
			if err := assertEqual(harnesstypes.SessionStats{MessageCount: 1, Usage: zeroUsage()}, firstResult.Stats, "first stats"); err != nil {
				return err
			}
			if err := assertEqual(harnesstypes.SessionStats{MessageCount: 2, Usage: zeroUsage()}, secondResult.Stats, "second stats"); err != nil {
				return err
			}
			if err := assertCommitStats(f.Storage, secondResult); err != nil {
				return err
			}
			entries, err := f.Storage.ScanEntries(harnesstypes.EntryScan{Order: orderPointer("asc")}, ctx)
			if err != nil {
				return err
			}
			return assertEqual([]string{"first", "second"}, entryIDs(entries), "entries")
		}),
		createStorageCase(factory, "lifecycle", "seals admission, drains admitted commits, and closes idempotently", func(f sessiontesting.StorageFixture) error {
			admitted := make(chan struct{})
			go func() {
				_, _ = f.Storage.Commit([]harnesstypes.Write{session.InsertEntry(userEntry("admitted", nil, "admitted"))}, ctx)
				close(admitted)
			}()
			firstClose := f.Storage.Close(ctx)
			secondClose := f.Storage.Close(ctx)
			if _, err := f.Storage.GetStats(ctx); err == nil {
				return fmt.Errorf("expected reads to reject after close")
			}
			if _, err := f.Storage.Commit(nil, ctx); err == nil {
				return fmt.Errorf("expected commits to reject after close")
			}
			<-admitted
			if firstClose != nil || secondClose != nil {
				return fmt.Errorf("close should be idempotent")
			}
			return nil
		}),
	}
}

func structureIDs(structure []harnesstypes.EntryStructure) []string {
	out := make([]string, 0, len(structure))
	for _, entry := range structure {
		out = append(out, entry.ID)
	}
	return out
}

func rowIDs(rows []harnesstypes.UsageRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }
func orderPointer(value string) *string  { return &value }
func entryTypePointer(value harnesstypes.EntryType) *harnesstypes.EntryType {
	return &value
}
