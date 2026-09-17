// Package cache holds what nanoDLNA has already worked out about a media file,
// so that rescanning a library does not repeat expensive work that is still
// valid.
//
// Entries are keyed by the media file's path and stamped with its size and
// modification time. That pair is what makes an entry trustworthy: a torrent
// client finishing a download changes both, while a file nobody has touched
// keeps its entry. Nothing here ever deletes or rewrites the media itself.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// dirName is the folder created in the user's home directory.
const dirName = ".nanoDLNA"

// Dir returns the cache directory for a media root.
//
// The location is ~/.nanoDLNA/cache/<hash>, which on Windows resolves through
// os.UserHomeDir to %USERPROFILE%\.nanoDLNA\cache\<hash> without needing a
// separate code path. The hash is of the absolute media root, so two libraries
// on one machine never share entries.
func Dir(mediaRoot string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot find the home directory for the cache: %w", err)
	}

	abs, err := filepath.Abs(mediaRoot)
	if err != nil {
		// A root that cannot be made absolute is still usable as a key.
		abs = mediaRoot
	}
	sum := sha256.Sum256([]byte(abs))

	return filepath.Join(home, dirName, "cache", hex.EncodeToString(sum[:8])), nil
}

// ThumbFile is the cache path for the thumbnail of src.
//
// The source's size and modification time are part of the file name, so a stale
// thumbnail can never be served as a current one: editing the video changes the
// name, and asking whether a thumbnail exists is a single stat with no index to
// consult and no way for the index to disagree with the disk.
//
// The cost is that a changed video leaves its old thumbnail behind, which Sweep
// is for.
func ThumbFile(dir, src string, size int, srcSize int64, mod time.Time) string {
	sum := sha256.Sum256([]byte(src))
	name := fmt.Sprintf("%s-%d-%d-%d.jpg", hex.EncodeToString(sum[:16]), size, srcSize, mod.Unix())
	return filepath.Join(dir, "thumbs", name)
}

// WriteFileAtomic writes data to path through a temporary file in the same
// directory that is then renamed over the target.
//
// A reader therefore never sees a half-written file, and an interrupted write
// leaves whatever was there before rather than truncating it.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// A no-op once the rename has succeeded.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Sweep removes files below root that keep does not name, returning how many it
// deleted. Directories are left in place.
//
// It is how a thumbnail for a video that was renamed, re-downloaded or deleted
// stops taking up space.
func Sweep(root string, keep map[string]bool) (int, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	removed := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A single unreadable entry should not abort the sweep.
			return nil //nolint:nilerr // deliberately skipping
		}
		if entry.IsDir() {
			return nil
		}
		if keep[path] {
			return nil
		}
		if removeErr := os.Remove(path); removeErr == nil {
			removed++
		}
		return nil
	})
	return removed, err
}
