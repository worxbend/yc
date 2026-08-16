package cli

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/chatlog"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/youtube"
)

// superChatCSVHeader is the export's column contract, in order. It is a
// package-level constant because the test asserting the format and the writer
// producing it must not be able to drift apart.
var superChatCSVHeader = []string{
	"timestamp", "chat_id", "author", "amount_value", "currency", "tier", "message",
}

// runExport dispatches `yc export <what>`. Today the only ledger worth
// exporting is the paid one: Super Chats and Super Stickers from the opt-in
// chat log, as CSV for a spreadsheet or an accounting tool.
func runExport(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || hasHelpArg(args) {
		fmt.Fprint(stdout, exportUsage)
		return ExitOK
	}
	switch what := strings.TrimSpace(args[0]); what {
	case "superchats":
		return runExportSuperchats(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown export %q\n\n", what)
		fmt.Fprint(stderr, exportUsage)
		return ExitUsage
	}
}

const exportUsage = `usage: yc export superchats [--dir DIR] [--out FILE] [--config PATH]

Reads the chat logs written when chat_logging is enabled and emits one CSV row
per Super Chat or Super Sticker: timestamp, chat id, author, amount value,
currency, tier, and the buyer's message.

  --dir DIR      read logs from DIR instead of the configured chat_log_dir
  --out FILE     write the CSV to FILE instead of standard output
  --config PATH  config file path
`

// runExportSuperchats reads every chat log file and writes the paid ledger.
//
// It performs no network work and spends no quota: everything it reports was
// recorded while chat was open. Running it with logging disabled or an empty
// log directory yields a header-only CSV rather than an error, because "no
// Super Chats" is an answer, not a failure.
func runExportSuperchats(args []string, stdout, stderr io.Writer) int {
	var dirFlag, outFlag, cfgPath string
	fs := flag.NewFlagSet("export superchats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&dirFlag, "dir", "", "chat log directory to read")
	fs.StringVar(&outFlag, "out", "", "output file; empty writes to stdout")
	fs.StringVar(&cfgPath, "config", "", "config file path")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		return ExitUsage
	}

	dir := strings.TrimSpace(dirFlag)
	if dir == "" {
		cfg, err := config.Load(os.Environ(), config.Overrides{ConfigPath: cfgPath})
		if err != nil {
			fmt.Fprintf(stderr, "load config: %s\n", config.RedactDisplayValue(err.Error()))
			return ExitFailure
		}
		dir = chatLogDir(cfg)
	}
	if dir == "" {
		fmt.Fprintln(stderr, "no chat log directory is available; set chat_log_dir or pass --dir")
		return ExitUsage
	}

	out := io.Writer(stdout)
	var outFile *os.File
	if path := strings.TrimSpace(outFlag); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Fprintf(stderr, "create output: %s\n", config.RedactDisplayValue(err.Error()))
			return ExitFailure
		}
		outFile = file
		out = file
	}

	rows, skipped, err := exportSuperchatsCSV(dir, out)
	if err != nil {
		if outFile != nil {
			_ = outFile.Close()
		}
		fmt.Fprintf(stderr, "export superchats: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}
	if outFile != nil {
		// Close is where a delayed write error (a full disk, a quota hit)
		// finally surfaces, so a failure here means the CSV is incomplete.
		if err := outFile.Close(); err != nil {
			fmt.Fprintf(stderr, "close output: %s\n", config.RedactDisplayValue(err.Error()))
			return ExitFailure
		}
	}
	if skipped > 0 {
		// A damaged line is skipped rather than ending the read, so the export
		// is complete apart from those lines. Saying so is the difference
		// between a known-partial ledger and one quietly missing rows.
		fmt.Fprintf(stderr, "skipped %d unreadable chat log line(s)\n", skipped)
	}
	fmt.Fprintf(stderr, "wrote %d super chat rows from %s\n", rows, config.RedactDisplayValue(dir))
	return ExitOK
}

// exportSuperchatsCSV streams every paid event from dir's logs into one CSV,
// returning how many data rows it wrote. Files are read oldest first, which
// keeps the ledger in roughly chronological order without loading everything
// into memory to sort it.
func exportSuperchatsCSV(dir string, out io.Writer) (int, int, error) {
	writer := csv.NewWriter(out)
	if err := writer.Write(superChatCSVHeader); err != nil {
		return 0, 0, err
	}

	rows := 0
	skipped := 0
	for _, path := range chatlog.ListLogFiles(dir) {
		file, err := os.Open(path)
		if err != nil {
			// A file that vanished between listing and opening (rotation
			// pruned it) is not a reason to abandon the rest.
			continue
		}
		fileSkipped, err := chatlog.Records(file, func(event chatlog.Event) error {
			if !isSuperChatEvent(event) {
				return nil
			}
			rows++
			return writer.Write(superChatCSVRow(event))
		})
		skipped += fileSkipped
		_ = file.Close()
		if err != nil {
			return rows, skipped, err
		}
	}
	writer.Flush()
	return rows, skipped, writer.Error()
}

// isSuperChatEvent selects the paid events the ledger reports: Super Chats and
// Super Stickers, echo and backlog rows included, because money was paid
// whether or not yc watched it live. Gifts and fan funding are excluded on
// purpose - they carry no per-event currency amount in the normalized model.
func isSuperChatEvent(event chatlog.Event) bool {
	switch youtube.EventKind(event.Kind) {
	case youtube.EventKindSuperChat, youtube.EventKindSuperSticker:
		return true
	default:
		return false
	}
}

// superChatCSVRow renders one event in header order. The amount is derived
// from the integer micros so the CSV holds a clean decimal ("5.00"), while the
// message column keeps whatever text the buyer attached.
func superChatCSVRow(event chatlog.Event) []string {
	timestamp := ""
	if !event.Timestamp.IsZero() {
		timestamp = event.Timestamp.UTC().Format(time.RFC3339)
	}
	return []string{
		timestamp,
		event.ChatID,
		event.Author,
		formatMicros(event.AmountMicros),
		event.Currency,
		strconv.Itoa(event.Tier),
		event.Text,
	}
}

// formatMicros renders millionths of a currency unit as a decimal with two
// places, trimming only trailing sub-cent digits ("5000000" -> "5.00",
// "1990000" -> "1.99", "1234567" -> "1.234567"). Integer arithmetic
// throughout: a float would round someone's money.
func formatMicros(micros int64) string {
	negative := micros < 0
	if negative {
		micros = -micros
	}
	whole := micros / 1_000_000
	fraction := micros % 1_000_000
	text := fmt.Sprintf("%d.%06d", whole, fraction)
	// Keep at least two decimal places, then drop trailing zeros beyond them.
	for strings.HasSuffix(text, "0") && !strings.HasSuffix(text[:len(text)-1], ".") {
		if dot := strings.IndexByte(text, '.'); len(text)-dot-1 <= 2 {
			break
		}
		text = text[:len(text)-1]
	}
	if negative {
		return "-" + text
	}
	return text
}
