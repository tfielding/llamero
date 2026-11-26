// Package atomicfile writes a file atomically and durably, and names files so ids don't
// appear on disk as names.
package atomicfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// HashedPath returns the file name for id in dir: a hash of the id, so ids (phone numbers,
// telegram ids) don't appear on disk as names.
func HashedPath(dir, id string) string {
	h := sha256.Sum256([]byte(id))
	return filepath.Join(dir, hex.EncodeToString(h[:8])+".json")
}

// Write writes data to path atomically and durably: the temp file is fsynced before the
// rename, and the directory after it, so a crash doesn't lose or corrupt the last write.
func Write(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// Remove deletes path, syncing the directory afterwards so the delete is durable. A missing
// file is not an error.
func Remove(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir fsyncs a directory, so a rename or remove that just happened is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
