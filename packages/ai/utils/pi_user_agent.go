// This file is a Go port of packages/ai/src/utils/pi-user-agent.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Upstream probes node:os lazily and falls back to "pi (browser)" when no runtime
// OS module is available, to keep browser/Vite builds working. A Go program
// always has an OS, so the runtime-adaptation is that the runtime OS/arch are
// always reported; the "browser" branch cannot occur.
package utils

import (
	"runtime"
)

// GetPiUserAgent returns the pi user-agent string: `pi (platform release; arch)`.
func GetPiUserAgent() string {
	return "pi (" + runtime.GOOS + " " + runtimeRelease() + "; " + runtime.GOARCH + ")"
}

// runtimeRelease is the OS release. Go does not expose the kernel release, so
// the Go runtime version stands in for the platform version string. This is a
// deliberate difference (it is only a display value in the user agent).
func runtimeRelease() string {
	return runtime.Version()
}
