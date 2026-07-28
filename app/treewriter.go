package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// treeWriter materializes prompt files under one destination directory, skipping whatever is already
// there. It is the single implementation `revmux init` and `--dump-defaults` both write through: the two
// differ in which layer they read and in what they report, never in how a file lands on disk.
//
// Every write goes through an os.Root on the destination, the way task.Scaffold writes through the tasks
// root. Joining the path and writing to it contains the last component alone — os.Lstat dereferences every
// directory above it, so a symlinked subdirectory sends the whole write wherever it points while the paths
// reported back still name the destination.
type treeWriter struct {
	dir  string
	root *os.Root
}

// newTreeWriter creates dir when it is absent and opens it as the root every write is contained in.
func newTreeWriter(dir string) (*treeWriter, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return &treeWriter{dir: dir, root: root}, nil
}

// close releases the destination handle.
func (w *treeWriter) close() error {
	if err := w.root.Close(); err != nil {
		return fmt.Errorf("close %s: %w", w.dir, err)
	}
	return nil
}

// write creates relPath — slash-separated and relative to the destination — reporting whether this call
// created it. An entry already there is left exactly as it is, a dangling symlink included: Lstat rather
// than Stat is what makes such a link an entry rather than a missing file, and O_EXCL refuses one planted
// between the look and the write.
func (w *treeWriter) write(relPath string, data []byte) (bool, error) {
	if dir := path.Dir(relPath); dir != "." {
		// an escaping link occupying a directory name reports ErrExist here and is named by the entry
		// check below, which says where the path leaves the destination rather than that it is taken
		if err := w.root.MkdirAll(dir, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return false, fmt.Errorf("create %s: %w", w.dest(dir), err)
		}
	}

	dst := w.dest(relPath)
	_, err := w.root.Lstat(relPath)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", dst, err)
	}

	f, err := w.root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write %s: %w", dst, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("write %s: %w", dst, err)
	}
	return true, nil
}

// dest renders one entry the way every message and every reported path names it.
func (w *treeWriter) dest(relPath string) string {
	return filepath.Join(w.dir, filepath.FromSlash(relPath))
}
