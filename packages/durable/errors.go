package durable

import (
	"errors"
	"fmt"
)

// ReadAfterWrite reports that a transaction read a table after its first table
// write. Read every required row before writing.
type ReadAfterWrite struct {
	Method string
}

func (e *ReadAfterWrite) Error() string {
	return fmt.Sprintf("Tx.%s() cannot read tables after the first table write", e.Method)
}

// StorageRejected reports that a storage batch definitely had no durable
// effect, so the owning Session may continue safely. It is not a general
// wrapper for uncertain write errors.
type StorageRejected struct {
	Message string
	Cause   error
}

func (e *StorageRejected) Error() string { return e.Message }

// Unwrap exposes the optional underlying cause.
func (e *StorageRejected) Unwrap() error { return e.Cause }

// ConversationBusy reports that a submission reached a busy conversation and was
// not admitted.
type ConversationBusy struct {
	ConversationID ConversationID
}

func (e *ConversationBusy) Error() string {
	return fmt.Sprintf("Conversation %d is busy", e.ConversationID)
}

// ErrStorageClosed is the sentinel base error reported by a closed storage
// adapter. Use errors.Is to test it.
var ErrStorageClosed = errors.New("storage is closed")

// ClosedError reports an operation on a closed storage adapter while matching
// ErrStorageClosed under errors.Is.
type ClosedError struct {
	Name string
}

func (e *ClosedError) Error() string { return e.Name + " is closed" }

// Is reports membership in the ErrStorageClosed sentinel family.
func (e *ClosedError) Is(target error) bool { return target == ErrStorageClosed }

// PoisonedError reports that an uncertain append or sync failure left an open
// storage backend in an unknown state. Reopen decides the confirmed durable
// state. The optional cause is exposed through Unwrap.
type PoisonedError struct {
	Message string
	Cause   error
}

func (e *PoisonedError) Error() string { return e.Message }

// Unwrap exposes the optional underlying cause.
func (e *PoisonedError) Unwrap() error { return e.Cause }
