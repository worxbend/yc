package chatlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/worxbend/yc/internal/youtube"
)

const (
	// DefaultMaxFileBytes is the rotation threshold when none is configured.
	// Ten megabytes is roughly a full day of very busy chat, so an ordinary
	// session produces exactly one file.
	DefaultMaxFileBytes int64 = 10 << 20
	// DefaultMaxFiles is how many log files are kept in total, current file
	// included, when none is configured.
	DefaultMaxFiles = 5
	// logFileSuffix marks the files this writer owns. Pruning only ever
	// removes files carrying it, so pointing the log at a populated
	// directory cannot delete someone else's data.
	logFileSuffix = ".jsonl"
	// logFilePrefix starts every file name the writer creates.
	logFilePrefix = "chat-"
	// logDirMode and logFileMode keep chat logs private, matching
	// internal/storage: a chat log says what the user was watching and when.
	logDirMode  fs.FileMode = 0o700
	logFileMode fs.FileMode = 0o600
)

// ErrWriterClosed reports an append after Close.
var ErrWriterClosed = errors.New("chat log writer is closed")

// Options configures a Writer. Only Dir is required.
type Options struct {
	// Dir is where log files are created. It is created 0700 on first use.
	Dir string
	// MaxFileBytes rotates the current file once it exceeds this size.
	// Non-positive means DefaultMaxFileBytes.
	MaxFileBytes int64
	// MaxFiles is how many files survive a rotation, current file included.
	// Non-positive means DefaultMaxFiles.
	MaxFiles int
	// Now is injectable for tests. Nil means time.Now.
	Now func() time.Time
	// Redact scrubs every free-text field before it is written. Nil means
	// no scrubbing. The normalized message model carries no credentials by
	// construction; this is the second line of defence for a secret pasted
	// into chat by the user themselves.
	Redact func(string) string
}

// Writer appends normalized chat events to a session's JSONL file.
//
// It is safe for concurrent use, and it is failure-tolerant by contract: an
// Append error leaves the writer usable, and the caller decides whether to
// keep trying. Nothing here can end a chat session.
type Writer struct {
	opts Options

	mu     sync.Mutex
	file   *os.File
	size   int64
	closed bool
}

// NewWriter returns a writer that will log into dir. No file is created until
// the first Append, so enabling logging costs nothing while chat is silent.
func NewWriter(opts Options) (*Writer, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, errors.New("chat log: directory is required")
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Writer{opts: opts}, nil
}

// Append writes one normalized message as one JSONL line, rotating first when
// the current file is over budget.
func (w *Writer) Append(message youtube.Message) error {
	event := FromMessage(message)
	if w.opts.Redact != nil {
		event.Text = w.opts.Redact(event.Text)
		event.Author = w.opts.Redact(event.Author)
		event.AmountDisplay = w.opts.Redact(event.AmountDisplay)
	}
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode chat log event: %w", err)
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWriterClosed
	}
	if err := w.ensureFileLocked(int64(len(line))); err != nil {
		return err
	}
	n, err := w.file.Write(line)
	w.size += int64(n)
	if err != nil {
		return fmt.Errorf("write chat log: %w", err)
	}
	return nil
}

// Close closes the current file. Further appends return ErrWriterClosed.
// It is safe to call more than once.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// ensureFileLocked opens the session file on first use and rotates when the
// pending write would leave the current file over budget.
func (w *Writer) ensureFileLocked(pending int64) error {
	if w.file != nil && w.size+pending > w.opts.MaxFileBytes {
		_ = w.file.Close()
		w.file = nil
		w.size = 0
	}
	if w.file != nil {
		return nil
	}
	if err := os.MkdirAll(w.opts.Dir, logDirMode); err != nil {
		return fmt.Errorf("prepare chat log directory: %w", err)
	}
	path, err := w.newFilePath()
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, logFileMode)
	if err != nil {
		return fmt.Errorf("create chat log: %w", err)
	}
	w.file = file
	w.size = 0
	w.pruneLocked()
	return nil
}

// newFilePath names the next log file after the moment it starts. A numeric
// suffix disambiguates two files started within the same second, which is
// what a rotation triggered by a message flood looks like.
func (w *Writer) newFilePath() (string, error) {
	stamp := w.opts.Now().UTC().Format("20060102-150405")
	base := filepath.Join(w.opts.Dir, logFilePrefix+stamp)
	for i := 0; i < 1000; i++ {
		candidate := base + logFileSuffix
		if i > 0 {
			candidate = fmt.Sprintf("%s.%d%s", base, i, logFileSuffix)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", errors.New("chat log: could not find an unused file name")
}

// pruneLocked deletes the oldest owned files beyond the retention count. The
// file names sort chronologically by construction, so name order is age order.
// Prune failures are ignored: retention is housekeeping, and a log that grows
// one file too many is better than an append that fails over it.
func (w *Writer) pruneLocked() {
	owned := ListLogFiles(w.opts.Dir)
	if len(owned) <= w.opts.MaxFiles {
		return
	}
	for _, path := range owned[:len(owned)-w.opts.MaxFiles] {
		_ = os.Remove(path)
	}
}

// ListLogFiles returns the chat log files under dir, oldest first. Only files
// this package's writer would have created are named, so a caller iterating
// them cannot be handed someone else's data.
func ListLogFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var owned []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() &&
			strings.HasPrefix(name, logFilePrefix) &&
			strings.HasSuffix(name, logFileSuffix) {
			owned = append(owned, filepath.Join(dir, name))
		}
	}
	sort.Strings(owned)
	return owned
}
