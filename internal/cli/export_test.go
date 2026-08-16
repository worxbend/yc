package cli

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLogFile drops a chat log fixture into dir under a name the chatlog
// package would have used, so ListLogFiles picks it up.
func writeLogFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

const superChatLine = `{"ts":"2026-08-15T12:00:00Z","chat_id":"c1","message_id":"m2","kind":"super_chat","type":"paid","author":"alice","text":"great stream","amount_micros":5000000,"currency":"USD","amount_display":"$5.00","tier":3}`

func TestExportSuperchatsSelectsOnlyPaidEventsAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	writeLogFile(t, dir, "chat-20260815-110000.jsonl", strings.Join([]string{
		`{"ts":"2026-08-15T11:00:00Z","chat_id":"c1","message_id":"m1","kind":"text","type":"chat","author":"bob","text":"hello"}`,
		superChatLine,
	}, "\n")+"\n")
	writeLogFile(t, dir, "chat-20260815-120000.jsonl",
		`{"ts":"2026-08-15T12:30:00Z","chat_id":"c2","message_id":"m3","kind":"super_sticker","type":"paid","author":"carol","amount_micros":1990000,"currency":"EUR","tier":1}`+"\n")
	// A file without the chatlog naming is someone else's data and must be
	// ignored.
	writeLogFile(t, dir, "notes.jsonl", superChatLine+"\n")

	var out bytes.Buffer
	rows, _, err := exportSuperchatsCSV(dir, &out)
	if err != nil {
		t.Fatalf("exportSuperchatsCSV: %v", err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want 2", rows)
	}

	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("CSV records = %d, want header + 2", len(records))
	}
	if got, want := strings.Join(records[0], ","), strings.Join(superChatCSVHeader, ","); got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if got := records[1]; got[2] != "alice" || got[3] != "5.00" || got[4] != "USD" || got[5] != "3" || got[6] != "great stream" {
		t.Fatalf("super chat row = %v", got)
	}
	if got := records[2]; got[2] != "carol" || got[3] != "1.99" || got[4] != "EUR" || got[5] != "1" {
		t.Fatalf("super sticker row = %v", got)
	}
}

func TestExportSuperchatsEmptyDirectoryIsHeaderOnly(t *testing.T) {
	var out bytes.Buffer
	rows, _, err := exportSuperchatsCSV(t.TempDir(), &out)
	if err != nil {
		t.Fatalf("exportSuperchatsCSV: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rows = %d, want 0", rows)
	}
	if got := strings.TrimSpace(out.String()); got != strings.Join(superChatCSVHeader, ",") {
		t.Fatalf("output = %q, want header only", got)
	}
}

func TestRunExportSuperchatsWritesToTheOutFile(t *testing.T) {
	dir := t.TempDir()
	writeLogFile(t, dir, "chat-20260815-110000.jsonl", superChatLine+"\n")
	outPath := filepath.Join(t.TempDir(), "ledger.csv")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"export", "superchats", "--dir", dir, "--out", outPath}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(data), "alice") || !strings.Contains(string(data), "5.00") {
		t.Fatalf("output missing rows:\n%s", data)
	}
}

func TestRunExportUnknownSubjectIsAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"export", "everything"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("exit = %d, want usage", code)
	}
}

func TestFormatMicros(t *testing.T) {
	tests := []struct {
		micros int64
		want   string
	}{
		{0, "0.00"},
		{5_000_000, "5.00"},
		{1_990_000, "1.99"},
		{1_234_567, "1.234567"},
		{100_000, "0.10"},
		{-2_500_000, "-2.50"},
	}
	for _, tt := range tests {
		if got := formatMicros(tt.micros); got != tt.want {
			t.Errorf("formatMicros(%d) = %q, want %q", tt.micros, got, tt.want)
		}
	}
}
