// Package testing holds the reusable session storage and repository test
// helpers ported from packages/agent/src/harness/session/testing.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package testing

import (
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// StorageFixture is a fresh backend storage instance owned by one test case.
type StorageFixture struct {
	Storage harnesstypes.Storage
	Close   func() error
}

// ConformanceCase is a runner-independent conformance case.
type ConformanceCase struct {
	Group string
	Name  string
	Run   func() error
}

// StorageFixtureFunc builds one storage fixture.
type StorageFixtureFunc func() (StorageFixture, error)
