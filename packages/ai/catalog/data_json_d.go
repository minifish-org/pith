// Package catalog exposes the bundled static model catalogs of the AI SDK.
//
// This is a Go port of the data-json declaration (packages/ai/src/providers/
// data-json.d.ts) together with the generated provider catalogs and the
// programmatic image-model catalog from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The `data` directory is a frozen copy of the upstream packages/ai/src/providers/data
// catalog JSON (the MIT license text and the publication manifest included). It
// is embedded read-only; nothing in this package ever rewrites it.
package catalog

import "embed"

// catalogDataFS embeds the frozen catalog JSON assets. The publication
// .manifest.json is shipped as manifest.json because build outputs exclude
// dotfiles, matching the upstream data-json.d.ts wildcard JSON module
// declaration this file ports.
//
//go:embed data
var catalogDataFS embed.FS
