// Package durable is the optional Pith Durable SDK: shared records, explicit
// definitions, validation, and the atomic storage boundary that later Session,
// harness, and adapter stages build on.
//
// This is a native Go adaptation of the Pi Durable runtime at revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32 (packages/durable/src/*.ts,
// storage/*.ts, testing/*.ts). It is not a line-for-line or binary-compatible
// TypeScript API. The upstream implementation at that pinned commit is the
// authority; later Pico5 design documents are not a replacement source.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// # Ownership and layering
//
// The root package owns shared records, constructors and validation, typed
// document and task definitions, the Storage/Tx/TaskRuntime interfaces, and the
// Session kernel. Storage adapters (memory, JSONL, SQLite) implement the Storage
// interface and import the shared records; the root package never imports an
// adapter, which is what keeps the wide embedding facade free of dependency
// cycles.
//
// # Records
//
// Record fields marshal with the upstream lower-camel JSON field names, and JSON
// payloads retain unknown fields. Nil optional pointers and nil JsonObject values
// mean absent, never an error. Allocated IDs and commit sequences are positive
// safe integers at most MaxSafeInteger; RootConversationID is reserved and the
// first MintID call returns 2. Upstream idFromNumber/seqFromNumber are erased
// trusted casts; IDFromNumber and SeqFromNumber reproduce that behavior without
// imposing a new validation policy on already-trusted records.
//
// # Attachment to Chord
//
// Document values are strict Chord JSON. This package depends on
// github.com/minifish-org/pith/packages/chord for detached copies and canonical
// operations; it never re-implements Chord.
package durable
