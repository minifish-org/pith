// This file is the Go equivalent of testing/index.ts. Go has no re-export
// statement, so the barrel is flattened. The reusable types and decorators live
// in this package; the conformance case generators live in the conformance
// subpackage and are referenced directly there.
//
//	gating-storage.ts      -> GatingStorage, CommitDiscarded
//	instrumented-storage.ts -> InstrumentedStorage
//	storage-decorator.ts   -> StorageDecorator
//	types.ts               -> ConformanceCase, StorageFixture
//	conformance/storage.ts -> conformance.CreateStorageConformance
//	conformance/*.go       -> conformance.CreateSessionRepo*Conformance
package testing
