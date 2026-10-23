// Package trash implements the safe deletion model: deleting a file moves it
// into an app-managed trash directory with a manifest entry, restorable for 30
// days, after which it is purged. Overwrites first back up the current file
// into trash, and rolling back to a backup backs up the version it replaces.
// Nothing here deletes user data outright.
package trash

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dansarpong/ccmanager/internal/safeio"
	"github.com/dansarpong/ccmanager/internal/store"
)

// RetentionDays is how long a trashed file can be restored before it is purged.
const RetentionDays = 30

// ErrExists means a restore was refused because something is already at the
// original path.
var ErrExists = errors.New("already exists")

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
// A session transcript's sidecar folder moves with it. now is passed in (rather
// than read from the clock) so callers and tests control the timestamps. It
// returns the new entry's id.
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

// stash moves the file into a new trash entry (or, when move is false, copies
// it there) and records it. A move whose manifest row can't be written is put
// back, so a file is never left in the trash with no way to restore it.
func (t *Trash) stash(kind, path, sessionID, note string, now time.Time, move bool) (int64, error) {
	if err := os.MkdirAll(t.trashDir, 0o700); err != nil {
		return 0, fmt.Errorf("creating trash dir: %w", err)
	}

	sub, err := t.newEntryDir(now)
	if err != nil {
		return 0, err
	}
	dst := filepath.Join(sub, filepath.Base(path))
	steps := []step{{from: path, to: dst}}
	if side, ok := sidecar(path); ok && kind == KindSession {
		steps = append(steps, step{from: side, to: strings.TrimSuffix(dst, ".jsonl")})
	}
	if move {
		err = moveAll(steps)
	} else if err = copyFile(path, dst); err == nil {
		err = syncDir(sub)
	}
	if err != nil {
		if !move {
			_ = os.Remove(dst)
		}
		_ = os.Remove(sub)
		return 0, err
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
		if !move {
			_ = os.Remove(dst)
		} else if uerr := undo(steps); uerr != nil {
			return 0, fmt.Errorf("%w; moving it back failed too, it is in %s: %v", err, sub, uerr)
		}
		_ = os.Remove(sub)
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
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return dir, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("creating trash entry dir: %w", err)
		}
	}
}

// sidecar returns the folder Claude Code keeps beside a transcript (<id>.jsonl
// has <id>/ holding subagents/, tool-results/ and the like), and whether there
// is one. A trashed transcript keeps its sidecar beside it the same way.
func sidecar(transcript string) (string, bool) {
	dir, ok := strings.CutSuffix(transcript, ".jsonl")
	if !ok {
		return "", false
	}
	info, err := os.Lstat(dir)
	return dir, err == nil && info.IsDir()
}

// Restore moves a trashed file, and a session's sidecar folder, back to the
// original path and removes the manifest entry. It refuses to overwrite
// anything already at the original path, except for a backup: rolling back to
// one replaces the current file, which is first backed up as a new entry
// dated now.
func (t *Trash) Restore(id int64, now time.Time) (store.TrashEntry, error) {
	e, err := t.store.GetTrash(id)
	if err != nil {
		return store.TrashEntry{}, fmt.Errorf("looking up trash entry %d: %w", id, err)
	}
	if e.Kind == KindMemoryBackup && exists(e.OriginalPath) {
		return e, t.rollBack(e, now)
	}
	steps := []step{{from: e.TrashPath, to: e.OriginalPath}}
	if side, ok := sidecar(e.TrashPath); ok && e.Kind == KindSession {
		steps = append(steps, step{from: side, to: strings.TrimSuffix(e.OriginalPath, ".jsonl")})
	}
	for _, s := range steps {
		if exists(s.to) {
			return e, fmt.Errorf("cannot restore: %s %w", s.to, ErrExists)
		}
	}
	dirMode := os.FileMode(0o755)
	if e.Kind == KindSession {
		// Claude Code keeps transcripts owner-only; copies trashed by older
		// versions lost that mode.
		if err := os.Chmod(e.TrashPath, 0o600); err != nil {
			return e, fmt.Errorf("restricting restored transcript: %w", err)
		}
		dirMode = 0o700
	}
	if err := os.MkdirAll(filepath.Dir(e.OriginalPath), dirMode); err != nil {
		return e, fmt.Errorf("recreating original directory: %w", err)
	}
	if err := moveAll(steps); err != nil {
		return e, err
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

// purge deletes each entry's directory and manifest row. An entry that fails is
// left in place and reported, without stopping the rest.
func (t *Trash) purge(entries []store.TrashEntry) (int, error) {
	n := 0
	var errs []error
	for _, e := range entries {
		dir, err := t.entryDir(e.TrashPath)
		if err == nil {
			err = os.RemoveAll(dir)
		}
		if err == nil {
			err = t.store.DeleteTrash(e.ID)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("purging trash entry %d: %w", e.ID, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// entryDir returns the entry directory that holds trashPath. Trash paths come
// from the database and purge deletes the whole directory, so anything but a
// direct child of the trash dir is refused.
func (t *Trash) entryDir(trashPath string) (string, error) {
	dir := filepath.Dir(filepath.Clean(trashPath))
	rel, err := filepath.Rel(filepath.Clean(t.trashDir), dir)
	if err != nil || rel == "." || rel == ".." || strings.ContainsRune(rel, filepath.Separator) {
		return "", fmt.Errorf("%s is not in its own entry of the trash dir %s", trashPath, t.trashDir)
	}
	return dir, nil
}

// step is one move of a file or directory.
type step struct{ from, to string }

// moveAll performs steps in order. If one fails, the steps already done are
// undone, so either everything moves or nothing does.
func moveAll(steps []step) error {
	for i, s := range steps {
		if err := move(s.from, s.to); err != nil {
			if uerr := undo(steps[:i]); uerr != nil {
				return fmt.Errorf("%w; undoing the earlier moves failed too: %v", err, uerr)
			}
			return err
		}
	}
	return nil
}

// undo moves steps back, last first.
func undo(steps []step) error {
	var errs []error
	for i := len(steps) - 1; i >= 0; i-- {
		if err := move(steps[i].to, steps[i].from); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// rename is os.Rename, swappable so tests can force the cross-device fallback.
var rename = os.Rename

// move renames src to dst, which keeps the mode and is atomic. Across
// filesystems, where rename can't work, it copies src, syncs the copy to disk,
// then removes src.
func move(src, dst string) error {
	err := rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := copyAll(src, dst, info); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	if err := os.RemoveAll(src); err != nil {
		// A failed directory removal may have taken part of src: keep the
		// complete copy. A single file is removed all or nothing.
		if !info.IsDir() {
			_ = os.Remove(dst)
		}
		return fmt.Errorf("removing %s after copying it to %s: %w", src, dst, err)
	}
	return nil
}

// copyAll copies the file, directory tree or symlink at src to dst, which must
// not exist, keeping permissions and syncing what it writes. On failure it
// removes what it created.
func copyAll(src, dst string, info fs.FileInfo) error {
	switch {
	case info.Mode().IsRegular():
		return copyFile(src, dst)
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.Mkdir(dst, 0o700); err != nil {
			return err
		}
		if err := copyDirContents(src, dst, info.Mode().Perm()); err != nil {
			_ = os.RemoveAll(dst)
			return err
		}
		return nil
	default:
		return fmt.Errorf("cannot move %s: not a file, directory or symlink", src)
	}
}

func copyDirContents(src, dst string, perm fs.FileMode) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return err
		}
		if err := copyAll(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), info); err != nil {
			return err
		}
	}
	// Set last: a read-only directory would refuse the entries copied into it.
	if err := os.Chmod(dst, perm); err != nil {
		return err
	}
	return syncDir(dst)
}

// copyFile copies src (following a symlink) to a new file dst with the same
// permissions, synced to disk. It never overwrites an existing dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("copying %s -> %s: %w", src, dst, err)
	}
	return nil
}

// syncDir flushes a directory's entries to disk, so a file just created in it
// survives a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}
	return nil
}
