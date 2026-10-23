// Package trash implements the safe deletion model: deleting a file moves it
// into an app-managed trash directory with a manifest entry, restorable for 30
// days, after which it is purged. Overwrites first back up the current file
// into trash, and rolling back to a backup backs up the version it replaces.
// Nothing here deletes user data outright.
package trash

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/dansarpong/ccmanager/internal/safeio"
	"github.com/dansarpong/ccmanager/internal/store"
)

// RetentionDays is how long a trashed file can be restored before it is purged.
const RetentionDays = 30

// Kinds of trash entries.
const (
	KindSession      = "session"       // a deleted transcript
	KindMemoryBackup = "memory-backup" // a pre-overwrite backup of a memory file
	KindMemoryDelete = "memory-delete" // a deleted memory (e.g. auto-memory) file
)

// Trash manages move-to-trash, backup, restore, and purge against a store and a
// trash directory on disk.
type Trash struct {
	store    *store.Store
	trashDir string
}

// New returns a Trash backed by store, storing files under trashDir.
func New(st *store.Store, trashDir string) *Trash {
	return &Trash{store: st, trashDir: trashDir}
}

// MoveToTrash moves path into the trash directory and records a manifest entry.
// now is passed in (rather than read from the clock) so callers and tests
// control the timestamps. It returns the new entry's id.
func (t *Trash) MoveToTrash(kind, path, sessionID, note string, now time.Time) (int64, error) {
	return t.stash(kind, path, sessionID, note, now, true)
}

// BackupBeforeWrite copies path into the trash directory before it is
// overwritten, so the prior content is restorable. It is a no-op (returns 0)
// if the file does not yet exist.
func (t *Trash) BackupBeforeWrite(path string, now time.Time) (int64, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat before backup: %w", err)
	}
	return t.stash(KindMemoryBackup, path, "", "backup before overwrite", now, false)
}

// stash places a copy (or moves) the file into trash and records it. When move
// is true the original is removed after copying.
func (t *Trash) stash(kind, path, sessionID, note string, now time.Time, move bool) (int64, error) {
	if err := os.MkdirAll(t.trashDir, 0o755); err != nil {
		return 0, fmt.Errorf("creating trash dir: %w", err)
	}

	sub, err := t.newEntryDir(now)
	if err != nil {
		return 0, err
	}
	dst := filepath.Join(sub, filepath.Base(path))
	if err := copyFile(path, dst); err != nil {
		return 0, err
	}
	if move {
		if err := os.Remove(path); err != nil {
			return 0, fmt.Errorf("removing original after trashing: %w", err)
		}
	}

	entry := store.TrashEntry{
		Kind:         kind,
		OriginalPath: path,
		TrashPath:    dst,
		SessionID:    sessionID,
		DeletedAt:    now.UTC().Format(time.RFC3339),
		PurgeAfter:   now.AddDate(0, 0, RetentionDays).UTC().Format(time.RFC3339),
		Note:         note,
	}
	id, err := t.store.InsertTrash(entry)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// newEntryDir creates the directory for a new trash entry. Each entry gets its
// own subdirectory keyed by a timestamp, so files with the same basename never
// collide; a timestamp already taken is bumped rather than shared.
func (t *Trash) newEntryDir(now time.Time) (string, error) {
	for ns := now.UnixNano(); ; ns++ {
		dir := filepath.Join(t.trashDir, strconv.FormatInt(ns, 10))
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("creating trash entry dir: %w", err)
		}
	}
}

// Restore moves a trashed file back to its original path and removes the
// manifest entry. It refuses to overwrite a file that already exists at the
// original path, except for a backup: rolling back to one replaces the current
// file, which is first backed up as a new entry dated now.
func (t *Trash) Restore(id int64, now time.Time) (store.TrashEntry, error) {
	e, err := t.store.GetTrash(id)
	if err != nil {
		return store.TrashEntry{}, fmt.Errorf("looking up trash entry %d: %w", id, err)
	}
	if e.Kind == KindMemoryBackup && exists(e.OriginalPath) {
		return e, t.rollBack(e, now)
	}
	if exists(e.OriginalPath) {
		return e, fmt.Errorf("cannot restore: a file already exists at %s", e.OriginalPath)
	}
	if err := os.MkdirAll(filepath.Dir(e.OriginalPath), 0o755); err != nil {
		return e, fmt.Errorf("recreating original directory: %w", err)
	}
	if err := copyFile(e.TrashPath, e.OriginalPath); err != nil {
		return e, err
	}
	if err := os.Remove(e.TrashPath); err != nil {
		return e, fmt.Errorf("removing trashed copy after restore: %w", err)
	}
	_ = os.Remove(filepath.Dir(e.TrashPath)) // best-effort cleanup of the entry dir
	if err := t.store.DeleteTrash(id); err != nil {
		return e, err
	}
	return e, nil
}

// rollBack swaps a backup in for the file at its original path, backing up the
// current version first so the swap can itself be undone.
func (t *Trash) rollBack(e store.TrashEntry, now time.Time) error {
	data, err := os.ReadFile(e.TrashPath)
	if err != nil {
		return fmt.Errorf("reading backup: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(e.OriginalPath); err == nil {
		mode = info.Mode().Perm()
	} else if info, err := os.Stat(e.TrashPath); err == nil {
		mode = info.Mode().Perm()
	}
	if _, err := t.BackupBeforeWrite(e.OriginalPath, now); err != nil {
		return fmt.Errorf("backing up the current version: %w", err)
	}
	if err := safeio.AtomicWrite(e.OriginalPath, data, mode); err != nil {
		return err
	}
	if err := os.Remove(e.TrashPath); err != nil {
		return fmt.Errorf("removing backup after roll back: %w", err)
	}
	_ = os.Remove(filepath.Dir(e.TrashPath))
	return t.store.DeleteTrash(e.ID)
}

// exists reports whether anything, even a dangling symlink, is at path.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// PurgeExpired deletes trash entries whose retention window has passed. now is
// supplied so the launch path and tests can control the cutoff. It returns the
// number of entries purged.
func (t *Trash) PurgeExpired(now time.Time) (int, error) {
	expired, err := t.store.ExpiredTrash(now.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return t.purge(expired)
}

// EmptyNow purges every trash entry immediately.
func (t *Trash) EmptyNow() (int, error) {
	all, err := t.store.ListTrash()
	if err != nil {
		return 0, err
	}
	return t.purge(all)
}

func (t *Trash) purge(entries []store.TrashEntry) (int, error) {
	n := 0
	for _, e := range entries {
		// Remove the trashed file and its entry directory; ignore if already gone.
		if err := os.Remove(e.TrashPath); err != nil && !os.IsNotExist(err) {
			return n, fmt.Errorf("purging %s: %w", e.TrashPath, err)
		}
		_ = os.Remove(filepath.Dir(e.TrashPath))
		if err := t.store.DeleteTrash(e.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying %s -> %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", dst, err)
	}
	return nil
}
