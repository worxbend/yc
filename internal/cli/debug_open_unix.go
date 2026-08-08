//go:build unix

package cli

import (
	"errors"
	"os"
	"syscall"
)

// debugLogOpenPlatformNote describes the guarantee this build actually makes,
// and is reported by `yc doctor` so the guarantee is visible rather than
// assumed.
const debugLogOpenPlatformNote = "Unix debug log files are opened with O_NOFOLLOW on the final path and validated through the opened file descriptor."

// openDebugLogFile opens the debug log for append with owner-only permissions.
//
// The exclusive-create attempt comes first so a fresh file is created with the
// right mode by yc rather than adopted from whatever was already there. The
// append fallback opens with O_NOFOLLOW, so a symlink planted at the path is
// refused by the kernel rather than detected by a stat that a racing rename
// could have invalidated.
func openDebugLogFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, debugLogFileMode)
	if err == nil {
		if err := file.Chmod(debugLogFileMode); err != nil {
			return closeDebugLogFileWithError(file, debugLogOperationError("set permissions on", path, err))
		}
		if err := validateOpenedDebugLogFile(path, file); err != nil {
			return closeDebugLogFileWithError(file, err)
		}
		return file, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, debugLogOpenFileError(path, err)
	}

	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, debugLogOpenFileError(path, err)
	}
	if err := validateOpenedDebugLogFile(path, file); err != nil {
		return closeDebugLogFileWithError(file, err)
	}
	return file, nil
}

// debugLogOpenErrorIsSymlink reports the kernel's no-follow refusal.
func debugLogOpenErrorIsSymlink(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}
