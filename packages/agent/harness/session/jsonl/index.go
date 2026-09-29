// This file is the Go equivalent of jsonl/index.ts. Go has no re-export
// statement, so the barrel is flattened: the public symbols live in the files
// that own them and this file documents the mapping.
//
//	repo.ts    -> JsonlSessionRepo
//	storage.ts -> JsonlStorage
//	types.ts   -> JSONL_FORMAT_VERSION, JSONL_STORAGE_VERSION, JsonlStorageHeader,
//	              JsonlStorageOptions, JsonlSessionMetadata, JsonlSessionCreateOptions,
//	              JsonlSessionListOptions, JsonlSessionRepoOptions
package jsonl
