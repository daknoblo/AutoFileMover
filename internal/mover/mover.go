// Package mover moves files and directories to a destination, falling back to a
// copy+delete when the source and destination are on different filesystems
// (common with bind mounts / different volumes).
package mover

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Move moves src (a file or directory) into destDir. The basename of src is
// preserved. It returns the final destination path.
//
// If a colliding entry already exists in destDir, an error is returned so that
// nothing is silently overwritten.
func Move(src, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create destination: %w", err)
	}
	dest := filepath.Join(destDir, filepath.Base(src))
	if _, err := os.Lstat(dest); err == nil {
		return "", fmt.Errorf("destination already exists: %s", dest)
	}

	// Fast path: atomic rename on the same filesystem.
	if err := os.Rename(src, dest); err == nil {
		return dest, nil
	}

	// Slow path: copy across filesystems, then remove the source.
	if err := copyPath(src, dest); err != nil {
		// Clean up a partial copy.
		_ = os.RemoveAll(dest)
		return "", fmt.Errorf("copy: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		return dest, fmt.Errorf("copied but failed to remove source: %w", err)
	}
	return dest, nil
}

// Delete permanently removes a file or directory tree. It returns nil if the
// path is already gone.
func Delete(path string) error {
	err := os.RemoveAll(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// RemoveIfEmpty removes dir only when it contains no remaining entries. It is a
// no-op (returns nil) if the directory is missing or still has contents.
func RemoveIfEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(entries) > 0 {
		return nil
	}
	return os.Remove(dir)
}

// CheckWritable verifies that the process can actually create, move and delete
// a file under dir — the exact operations used when sorting media. It creates a
// tiny temp file, renames it and removes it, returning the first error.
func CheckWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".afm-write-test-*")
	if err != nil {
		return fmt.Errorf("cannot create files in %s: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	moved := name + ".moved"
	if err := os.Rename(name, moved); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("cannot move files in %s: %w", dir, err)
	}
	if err := os.Remove(moved); err != nil {
		return fmt.Errorf("cannot delete files in %s: %w", dir, err)
	}
	return nil
}

func copyPath(src, dest string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dest, info)
	}
	return copyFile(src, dest, info)
}

func copyDir(src, dest string, info os.FileInfo) error {
	if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dest, e.Name())
		ei, err := e.Info()
		if err != nil {
			return err
		}
		if ei.IsDir() {
			if err := copyDir(s, d, ei); err != nil {
				return err
			}
		} else {
			if err := copyFile(s, d, ei); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dest string, info os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
