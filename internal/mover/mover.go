// Package mover moves files and directories to a destination, falling back to a
// copy+delete when the source and destination are on different filesystems
// (common with bind mounts / different volumes).
//
// Every entry point takes a context. The individual kernel calls (os.Rename,
// os.RemoveAll) cannot be interrupted, but long-running work is chunked so a
// shutdown or a stalled network share does not block indefinitely.
package mover

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// copyChunk is how much is copied between two cancellation checks: large enough
// to keep throughput on a network share, small enough to react to a shutdown.
const copyChunk = 4 << 20 // 4 MiB

// Move moves src (a file or directory) into destDir. The basename of src is
// preserved. It returns the final destination path.
//
// If a colliding entry already exists in destDir, an error is returned so that
// nothing is silently overwritten.
func Move(ctx context.Context, src, destDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
	if err := copyPath(ctx, src, dest); err != nil {
		// Clean up the partial copy so a retry starts clean and no truncated
		// media file is left behind in the library.
		if rmErr := os.RemoveAll(dest); rmErr != nil && !os.IsNotExist(rmErr) {
			return "", fmt.Errorf("copy: %w (partial copy left at %s: %v)", err, dest, rmErr)
		}
		return "", fmt.Errorf("copy: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		return dest, fmt.Errorf("copied but failed to remove source: %w", err)
	}
	return dest, nil
}

// Delete permanently removes a file or directory tree. It returns nil if the
// path is already gone.
func Delete(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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

// RemoveEmptyDirs removes empty directories within root (deepest first) and root
// itself if it ends up empty. Directories that still contain files are kept, so
// it is safe to call on a partially-emptied source folder — it never deletes a
// directory that holds files. This cleans up the empty per-episode sub-folders
// a season pack leaves behind after its videos have been moved out.
func RemoveEmptyDirs(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var dirs []string
	var skipped []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if walkErr != nil {
			// A single unreadable entry must not abort the cleanup, but it is
			// reported so an unexpected permission problem stays visible.
			skipped = append(skipped, p)
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Process the deepest directories first so children are removed before their
	// parents and the emptiness cascades upward.
	sort.Slice(dirs, func(i, j int) bool {
		return strings.Count(dirs[i], string(os.PathSeparator)) > strings.Count(dirs[j], string(os.PathSeparator))
	})
	for _, dir := range dirs {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		entries, e := os.ReadDir(dir)
		if e != nil {
			skipped = append(skipped, dir)
			continue
		}
		if len(entries) == 0 {
			if rmErr := os.Remove(dir); rmErr != nil && !os.IsNotExist(rmErr) {
				skipped = append(skipped, dir)
			}
		}
	}
	if len(skipped) > 0 {
		return fmt.Errorf("cleanup skipped %d path(s), first: %s", len(skipped), skipped[0])
	}
	return nil
}

// CheckWritable verifies that the process can actually create, move and delete
// a file under dir — the exact operations used when sorting media. It creates a
// tiny temp file, renames it and removes it, returning the first error.
func CheckWritable(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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

func copyPath(ctx context.Context, src, dest string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(ctx, src, dest, info)
	}
	return copyFile(ctx, src, dest, info)
}

func copyDir(ctx context.Context, src, dest string, info os.FileInfo) error {
	if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dest, e.Name())
		ei, err := e.Info()
		if err != nil {
			return err
		}
		if ei.IsDir() {
			if err := copyDir(ctx, s, d, ei); err != nil {
				return err
			}
		} else {
			if err := copyFile(ctx, s, d, ei); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies one file and flushes it to stable storage before returning.
// The fsync matters because the caller removes the source right afterwards: an
// unflushed copy plus a deleted source loses the file on a power cut.
func copyFile(ctx context.Context, src, dest string, info os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if err := copyChunked(ctx, out, in); err != nil {
		_ = out.Close()
		if rmErr := os.Remove(dest); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("%w (partial file left at %s: %v)", err, dest, rmErr)
		}
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dest)
		return fmt.Errorf("flush %s: %w", dest, err)
	}
	return out.Close()
}

// copyChunked copies in fixed-size chunks, checking for cancellation between
// them so a stalled share or a shutdown does not block the whole transfer.
func copyChunked(ctx context.Context, dst io.Writer, src io.Reader) error {
	buf := make([]byte, copyChunk)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
