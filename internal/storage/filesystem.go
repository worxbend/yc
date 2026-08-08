package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// ErrPathIsDirectory reports that a path expected to name a file is a
// directory.
var ErrPathIsDirectory = errors.New("path is a directory")

// ErrPathNotRegular reports a path that is neither a regular file nor a
// directory - a symlink, a socket, a device - and therefore is not something yc
// will open.
var ErrPathNotRegular = errors.New("path is not a regular file")

// CheckReadableFile reports whether path is a regular file yc can read, and
// rejects symlinks and unexpected file types before anything opens them.
//
// Lstat rather than Stat is the point: a symlink pointing at a credential file
// must be reported as a symlink, not silently followed.
func CheckReadableFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return ErrPathIsDirectory
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrPathNotRegular, info.Mode().Type())
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

// ProbeWritableDir reports whether dir exists, has acceptable permissions, and
// accepts a write. It backs `yc doctor` and must never leave a file behind.
func ProbeWritableDir(dir string) error {
	if dir == "" {
		return errors.New("directory path is required")
	}
	if err := os.MkdirAll(dir, cacheDirMode); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: directory is a symlink", ErrPathNotRegular)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrPathIsDirectory, "expected a directory")
	}

	// O_EXCL so the probe can never truncate a file that already exists, and
	// a pid suffix so two yc processes probing at once do not collide.
	probePath := filepath.Join(dir, ".yc-doctor-write-test")
	file, err := os.OpenFile(probePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, cacheFileMode)
	if errors.Is(err, fs.ErrExist) {
		probePath = filepath.Join(dir, ".yc-doctor-write-test-"+strconv.Itoa(os.Getpid()))
		file, err = os.OpenFile(probePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, cacheFileMode)
	}
	if err != nil {
		return err
	}
	if _, err := file.Write([]byte("ok\n")); err != nil {
		_ = file.Close()
		_ = os.Remove(probePath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(probePath)
		return err
	}
	return os.Remove(probePath)
}
