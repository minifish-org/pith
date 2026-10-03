package durabletesting

import (
	"context"
	"testing"
)

// RegisterStorageConformance registers every source conformance case as a Go
// subtest of a subtest named name, using provider to open a fresh adapter per
// case. It is the native equivalent of the source registerStorageConformance
// runner adapter: the case bodies stay runner-independent while this helper
// binds them to the standard testing runner.
func RegisterStorageConformance(t *testing.T, name string, provider StorageProvider) {
	t.Helper()
	cases := CreateStorageConformance(provider)
	t.Run(name, func(t *testing.T) {
		for _, testCase := range cases {
			testCase := testCase
			t.Run(testCase.Name, func(t *testing.T) {
				if err := testCase.Run(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
}
