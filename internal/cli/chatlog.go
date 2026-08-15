package cli

import (
	"path/filepath"
	"strings"

	"github.com/worxbend/yc/internal/chatlog"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/debuglog"
)

// chatLogDir resolves where chat logs live: the configured directory, or
// "chatlog" under the platform cache directory when none is set. An empty
// return means the machine has no usable location at all.
func chatLogDir(cfg config.Config) string {
	if dir := strings.TrimSpace(cfg.Logging.ChatLogDir); dir != "" {
		return dir
	}
	base, err := config.DefaultCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "chatlog")
}

// openChatLogWriter builds the opt-in chat log writer, or nil when logging is
// off or nowhere to write exists.
//
// A nil writer is a supported state everywhere it flows: the app simply skips
// logging, which is the failure-tolerance the feature promises - a broken log
// location degrades to no log, never to a chat session that cannot start.
func openChatLogWriter(cfg config.Config, logger debuglog.Logger) *chatlog.Writer {
	if !cfg.Logging.ChatLogEnabled {
		return nil
	}
	dir := chatLogDir(cfg)
	if dir == "" {
		return nil
	}
	writer, err := chatlog.NewWriter(chatlog.Options{
		Dir:          dir,
		MaxFileBytes: int64(cfg.Logging.ChatLogMaxBytes),
		MaxFiles:     cfg.Logging.ChatLogMaxFiles,
		// The debug logger's redactor already knows every credential in
		// play this run; reusing it means a secret pasted into chat by the
		// streamer cannot land in the archive either.
		Redact: logger.Redact,
	})
	if err != nil {
		return nil
	}
	return writer
}
