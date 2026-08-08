//go:build !unix

package cli

import (
	"errors"
	"os"
)

// debugLogOpenPlatformNote states the weaker guarantee this build makes, so
// `yc doctor` reports it rather than implying Unix semantics everywhere.
const debugLogOpenPlatformNote = "Non-Unix debug log opening does not provide O_NOFOLLOW or exact owner-only ACL guarantees; it rejects the unsafe paths it can observe before and after open."

// openDebugLogFile opens the debug log for append.
//
// Without a portable no-follow flag this can only reject a symlink visible
// before the open and then re-validate the opened descriptor, which closes the
// window without eliminating it.
func openDebugLogFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, debugLogFileMode)
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

	if err := validateDebugLogPath(path); err != nil {
		return nil, err
	}
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return nil, debugLogOpenFileError(path, err)
	}
	if err := validateOpenedDebugLogFile(path, file); err != nil {
		return closeDebugLogFileWithError(file, err)
	}
	return file, nil
}

// debugLogOpenErrorIsSymlink has no portable equivalent here.
func debugLogOpenErrorIsSymlink(error) bool {
	return false
}
