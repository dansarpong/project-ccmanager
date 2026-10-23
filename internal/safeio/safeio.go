// Package safeio provides atomic file writes and on-disk change detection.
// Every write the app makes goes through here: content is written to a temp
// file in the same directory and renamed into place, so a reader never sees a
// half-written file. Change detection lets the UI warn when a file was edited
// on disk (e.g. by Claude Code) since the user opened it.
package safeio

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileState is a cheap fingerprint of a file, captured when it is opened for
// editing and compared at save time. It uses mtime + size rather than a
// content hash: that is enough to detect the edits this app guards against and
// avoids reading large files twice.
type FileState struct {
	Mtime int64 // unix nanoseconds
	Size  int64
}

// AtomicWrite writes data to path atomically: a temp file in the same directory
// is written, flushed, and renamed over path. Rename within a directory is
// atomic on the platforms this tool targets. If path is a symlink (e.g.
// CLAUDE.md -> AGENTS.md), the link's target is written and the link is kept.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ccmanager-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Clean up the temp file if anything below fails before the rename.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming temp file into place: %w", err)
	}
	return nil
}

// Snapshot captures a file's current state for later change detection.
func Snapshot(path string) (FileState, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileState{}, fmt.Errorf("snapshotting %s: %w", path, err)
	}
	return FileState{Mtime: info.ModTime().UnixNano(), Size: info.Size()}, nil
}

// Changed reports whether path differs from the given snapshot (or no longer
// exists). A file that cannot be stat'd is treated as changed.
func Changed(path string, prev FileState) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("checking %s for changes: %w", path, err)
	}
	cur := FileState{Mtime: info.ModTime().UnixNano(), Size: info.Size()}
	return cur != prev, nil
}
