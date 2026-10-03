package jsonl

import (
	"context"

	"github.com/minifish-org/pith/packages/durable/env"
)

// replacement is one planned sidecar rewrite. An empty content removes the
// file; any other content atomically replaces it.
type replacement struct {
	file    string
	content string
}

// reclaimSidecars performs best-effort sidecar maintenance after a marker has
// authorized it. In Fsync mode it flushes the authorizing main marker first: a
// failed flush defers reclamation to reopen rather than risking unconfirmed
// data. Per-file failures are ignored and retried on the next recover.
func (s *Storage) reclaimSidecars(replacements []replacement, ctx context.Context) {
	if len(replacements) == 0 {
		return
	}
	if s.fsync {
		if err := s.fs.FlushFile(ctx, s.mainPath); err != nil {
			return
		}
	}
	for _, item := range replacements {
		s.replaceSidecar(item, ctx)
	}
}

// replaceSidecar writes a replacement .reclaim file, flushes it when required,
// and renames it onto the live path. Removing an empty replacement deletes the
// live sidecar; the next append recreates it, so later writes never target an
// unlinked inode.
func (s *Storage) replaceSidecar(item replacement, ctx context.Context) {
	path, err := s.fs.JoinPath(ctx, s.directory, item.file)
	if err != nil {
		return
	}
	if item.content == "" {
		_ = s.fs.Remove(ctx, path, env.RemoveOptions{Force: true})
		return
	}
	temporaryPath, err := s.fs.JoinPath(ctx, s.directory, item.file+reclaimSuffix)
	if err != nil {
		return
	}
	if err := s.fs.WriteFile(ctx, temporaryPath, []byte(item.content)); err != nil {
		return
	}
	if s.fsync {
		if err := s.fs.FlushFile(ctx, temporaryPath); err != nil {
			return
		}
	}
	_ = s.fs.RenameFile(ctx, temporaryPath, path)
}
