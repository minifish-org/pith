// Package jsonl is the Go port of
// packages/agent/src/harness/session/jsonl/*.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package jsonl

import (
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// JSONL_FORMAT_VERSION is the on-disk session format version.
const JSONL_FORMAT_VERSION = 4

// JSONL_STORAGE_VERSION is the storage contract version.
const JSONL_STORAGE_VERSION = 1

// JsonlStorageHeader is the first JSONL record.
type JsonlStorageHeader struct {
	V                       int     `json:"v"`
	Kind                    string  `json:"kind"`
	ID                      string  `json:"id"`
	StorageVersion          int     `json:"storageVersion"`
	CreatedAt               float64 `json:"createdAt"`
	Cwd                     string  `json:"cwd"`
	ParentSessionID         *string `json:"parentSessionId,omitempty"`
	LegacyParentSessionPath *string `json:"legacyParentSessionPath,omitempty"`
	NextSeq                 *int    `json:"nextSeq,omitempty"`
}

// JsonlStorageOptions configure one JSONL storage instance.
type JsonlStorageOptions struct {
	FileSystem harnesstypes.FileSystem
	Path       string
	Now        func() float64
}

// JsonlSessionMetadata is the discovered metadata of one JSONL session.
type JsonlSessionMetadata struct {
	harnesstypes.SessionMetadata
	Path       string  `json:"path"`
	ModifiedAt float64 `json:"modifiedAt"`
	Cwd        string  `json:"cwd"`
}

// JsonlSessionCreateOptions configure JSONL session creation.
type JsonlSessionCreateOptions struct {
	harnesstypes.SessionCreateOptions
	Cwd string
}

// JsonlSessionListOptions scope a JSONL session listing.
type JsonlSessionListOptions struct {
	Cwd *string
}

// JsonlSessionRepoOptions configure a JSONL session repository.
type JsonlSessionRepoOptions struct {
	FileSystem   harnesstypes.FileSystem
	SessionsRoot string
	Now          func() float64
}
