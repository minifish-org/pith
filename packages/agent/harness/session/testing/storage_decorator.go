// This file carries testing/storage-decorator.ts: a forwarding base for
// test-only Storage decorators.
package testing

import (
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// StorageDecorator forwards every Storage operation to its delegate.
type StorageDecorator struct {
	Delegate harnesstypes.Storage
}

// NewStorageDecorator builds a forwarding decorator.
func NewStorageDecorator(delegate harnesstypes.Storage) StorageDecorator {
	return StorageDecorator{Delegate: delegate}
}

// Commit forwards one transaction.
func (d StorageDecorator) Commit(writes []harnesstypes.Write, ctx harnesstypes.Context) (harnesstypes.CommitResult, error) {
	return d.Delegate.Commit(writes, ctx)
}

// GetEntries forwards an entry read.
func (d StorageDecorator) GetEntries(ids []string, ctx harnesstypes.Context) (map[string]harnesstypes.Entry, error) {
	return d.Delegate.GetEntries(ids, ctx)
}

// GetValue forwards a scalar value read.
func (d StorageDecorator) GetValue(address harnesstypes.Value, ctx harnesstypes.Context) (harnesstypes.StoredValue, bool, error) {
	return d.Delegate.GetValue(address, ctx)
}

// ScanValues forwards a scalar prefix scan.
func (d StorageDecorator) ScanValues(prefix harnesstypes.Value, ctx harnesstypes.Context) ([]harnesstypes.StoredValue, error) {
	return d.Delegate.ScanValues(prefix, ctx)
}

// ReadList forwards a list read.
func (d StorageDecorator) ReadList(address harnesstypes.ValueList, options *harnesstypes.ListReadOptions, ctx harnesstypes.Context) ([]harnesstypes.ListElement, error) {
	return d.Delegate.ReadList(address, options, ctx)
}

// ScanBranch forwards a branch scan.
func (d StorageDecorator) ScanBranch(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	return d.Delegate.ScanBranch(query, ctx)
}

// ScanBranchStructure forwards a branch structure scan.
func (d StorageDecorator) ScanBranchStructure(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.EntryStructure, error) {
	return d.Delegate.ScanBranchStructure(query, ctx)
}

// ScanEntries forwards a global entry scan.
func (d StorageDecorator) ScanEntries(query harnesstypes.EntryScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	return d.Delegate.ScanEntries(query, ctx)
}

// ScanUsage forwards a usage scan.
func (d StorageDecorator) ScanUsage(query harnesstypes.UsageScan, ctx harnesstypes.Context) ([]harnesstypes.UsageRow, error) {
	return d.Delegate.ScanUsage(query, ctx)
}

// GetStats forwards the stats read.
func (d StorageDecorator) GetStats(ctx harnesstypes.Context) (harnesstypes.SessionStats, error) {
	return d.Delegate.GetStats(ctx)
}

// Close forwards close.
func (d StorageDecorator) Close(ctx harnesstypes.Context) error {
	return d.Delegate.Close(ctx)
}
