// This file is the Go equivalent of session/index.ts. Go has no re-export
// statement, so the barrel is flattened: the public symbols live in the files
// that own them and this file documents the mapping.
//
//	commit.ts               -> CommittedWrite family, PreparedCommit, CommitValidationState,
//	                          InsertEntry, InsertUsage, CommitWrite, MaterializeCommittedEntry,
//	                          PrepareStorageCommit, ValidateCommittedWrites
//	fork.ts                 -> CreateForkSnapshot, ForkSourceSnapshot, ForkDestinationSnapshot
//	fork-policy.ts          -> ForkCurrentStatePlan, SelectBranchFork, ProjectForkCurrentStateWrite
//	jsonl/index.ts          -> JsonlSessionRepo, JsonlStorageHeader, JSONL_STORAGE_VERSION, ...
//	memory.ts               -> MemorySessionRepo, MemorySessionRepoOptions, MemoryStorage,
//	                          MemoryStorageOptions
//	session.ts              -> StorageBackedSession, StorageBackedSessionOptions, Session*Error
//	values.ts               -> Value, ValueList, StoredValue, ListElement and the reserved
//	                          namespace helpers
//	context.ts              -> BuildSessionContext, BuildContextEntries, SessionContextBuildOptions
package session
