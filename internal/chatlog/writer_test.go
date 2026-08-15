package chatlog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/youtube"
)

func testMessage(id, text string) youtube.Message {
	return youtube.Message{
		ID:         id,
		LiveChatID: "chat-1",
		Timestamp:  time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
		Author: youtube.Author{
			ChannelID:   "UC-alice",
			DisplayName: "alice",
		},
		Text: text,
		Kind: youtube.EventKindText,
		Type: youtube.MessageTypeChat,
	}
}

func superChatMessage(id string, micros int64) youtube.Message {
	message := testMessage(id, "thanks for the stream")
	message.Kind = youtube.EventKindSuperChat
	message.Type = youtube.MessageTypePaid
	message.SuperChat = &youtube.SuperChatDetails{
		Amount: youtube.Money{Micros: micros, Currency: "USD", Display: "$5.00"},
		Tier:   3,
	}
	return message
}

func newTestWriter(t *testing.T, opts Options) (*Writer, string) {
	t.Helper()
	if opts.Dir == "" {
		opts.Dir = t.TempDir()
	}
	writer, err := NewWriter(opts)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	return writer, opts.Dir
}

func TestNewWriterRequiresADirectory(t *testing.T) {
	if _, err := NewWriter(Options{}); err == nil {
		t.Fatal("NewWriter accepted an empty directory")
	}
}

func TestAppendWritesOneJSONLinePerEvent(t *testing.T) {
	writer, dir := newTestWriter(t, Options{})
	for i, message := range []youtube.Message{
		testMessage("m1", "hello"),
		superChatMessage("m2", 5_000_000),
	} {
		if err := writer.Append(message); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	files := ListLogFiles(dir)
	if len(files) != 1 {
		t.Fatalf("log files = %d, want 1", len(files))
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2\n%s", len(lines), data)
	}
	if !strings.Contains(lines[0], `"message_id":"m1"`) || !strings.Contains(lines[0], `"text":"hello"`) {
		t.Fatalf("first line missing message fields: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"amount_micros":5000000`) ||
		!strings.Contains(lines[1], `"currency":"USD"`) ||
		!strings.Contains(lines[1], `"tier":3`) {
		t.Fatalf("super chat line missing amount fields: %s", lines[1])
	}
}

func TestAppendCreatesNoFileUntilTheFirstEvent(t *testing.T) {
	_, dir := newTestWriter(t, Options{})
	if files := ListLogFiles(dir); len(files) != 0 {
		t.Fatalf("files before first append = %d, want 0", len(files))
	}
}

func TestFilePermissionsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on windows")
	}
	writer, dir := newTestWriter(t, Options{})
	if err := writer.Append(testMessage("m1", "hello")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	// TempDir already exists, so only the file mode is guaranteed here; a
	// nested configured dir is covered below.
	files := ListLogFiles(dir)
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("log file mode = %o, want 600", got)
	}
	_ = dirInfo

	nested := filepath.Join(t.TempDir(), "logs")
	nestedWriter, _ := newTestWriter(t, Options{Dir: nested})
	if err := nestedWriter.Append(testMessage("m2", "hi")); err != nil {
		t.Fatalf("Append nested: %v", err)
	}
	nestedInfo, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat nested dir: %v", err)
	}
	if got := nestedInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("log dir mode = %o, want 700", got)
	}
}

func TestRotationStartsANewFileAndPrunesTheOldest(t *testing.T) {
	// Times advance one second per file so every rotation gets a distinct,
	// sortable name.
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	writer, dir := newTestWriter(t, Options{
		// Small enough that every appended line overflows the previous
		// file, so each append after the first rotates.
		MaxFileBytes: 150,
		MaxFiles:     2,
		Now:          clock,
	})

	for i := 0; i < 5; i++ {
		if err := writer.Append(testMessage("m", strings.Repeat("x", 120))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	files := ListLogFiles(dir)
	if len(files) > 2 {
		t.Fatalf("retained files = %d, want <= 2 (MaxFiles)", len(files))
	}
	if len(files) < 2 {
		t.Fatalf("retained files = %d; rotation never happened", len(files))
	}
}

func TestAppendAfterCloseReportsClosed(t *testing.T) {
	writer, _ := newTestWriter(t, Options{})
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := writer.Append(testMessage("m1", "hello")); err != ErrWriterClosed {
		t.Fatalf("Append after Close = %v, want ErrWriterClosed", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestRedactorScrubsFreeTextFields(t *testing.T) {
	writer, dir := newTestWriter(t, Options{
		Redact: func(s string) string {
			return strings.ReplaceAll(s, "sekret-token", "[redacted]")
		},
	})
	if err := writer.Append(testMessage("m1", "my key is sekret-token oops")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	data, err := os.ReadFile(ListLogFiles(dir)[0])
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), "sekret-token") {
		t.Fatal("redacted value reached disk")
	}
	if !strings.Contains(string(data), "[redacted]") {
		t.Fatalf("placeholder missing: %s", data)
	}
}

func TestFromMessageFlattensAmounts(t *testing.T) {
	tests := []struct {
		name       string
		message    youtube.Message
		wantMicros int64
		wantTier   int
	}{
		{name: "plain text", message: testMessage("m1", "hi")},
		{name: "super chat", message: superChatMessage("m2", 1_990_000), wantMicros: 1_990_000, wantTier: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := FromMessage(tt.message)
			if event.MessageID != tt.message.ID {
				t.Fatalf("MessageID = %q, want %q", event.MessageID, tt.message.ID)
			}
			if event.AmountMicros != tt.wantMicros {
				t.Fatalf("AmountMicros = %d, want %d", event.AmountMicros, tt.wantMicros)
			}
			if event.Tier != tt.wantTier {
				t.Fatalf("Tier = %d, want %d", event.Tier, tt.wantTier)
			}
		})
	}
}

func TestRecordsSkipsACorruptTailButKeepsCompleteRecords(t *testing.T) {
	input := `{"ts":"2026-08-15T12:00:00Z","chat_id":"c1","kind":"text","type":"chat","text":"hello"}
{"ts":"2026-08-15T12:00:01Z","chat_id":"c1","kind":"text","ty` // truncated mid-line
	var texts []string
	err := Records(strings.NewReader(input), func(event Event) error {
		texts = append(texts, event.Text)
		return nil
	})
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(texts) != 1 || texts[0] != "hello" {
		t.Fatalf("texts = %v, want the one complete record", texts)
	}
}
