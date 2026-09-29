// Barrel package for the telemetry adapter conformance helpers.
//
// This is a Go port of packages/telemetry/src/testing/index.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Go does not mechanically mirror JS barrels, but this file keeps the upstream
// index.ts symbol ownership: the conformance builder surfaces through the
// testing package alongside the fixture types declared in types.go.
package testing

// The conformance builder and fixture types are declared in conformance.go and
// types.go and are exported directly from this package, matching the upstream
// index.ts re-export surface:
//
//	CreateTelemetryAdapterConformance  (conformance.go)
//	TelemetryAdapterConformanceCase    (types.go)
//	TelemetryAdapterFixture            (types.go)
//	TelemetryAdapterFixtureFactory     (types.go)
