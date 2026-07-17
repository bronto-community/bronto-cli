package cli

import (
	"bufio"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/ingest"
)

func newSendCmd() *cobra.Command {
	var (
		dataset       string
		collection    string
		tags          []string
		message       string
		ingestURLFlag string
		batchSize     int
		batchBytes    int
		flushInterval time.Duration
		noGzip        bool
	)
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send events into Bronto (one-shot message or NDJSON stream from stdin)",
		Long: "Sends events into Bronto's ingestion API. With -m/--message, sends exactly one\n" +
			"event and exits. Otherwise reads NDJSON or plain-text lines from stdin, batching\n" +
			"them by --batch-size/--batch-bytes and flushing on a --flush-interval ticker so\n" +
			"'tail -f access.log | bronto send -d app' ships promptly. If a batch fails to\n" +
			"send, that error aborts the command; events already in flight for that batch are\n" +
			"lost (retry the command; batches are not deduplicated).",
		Example: "  bronto send -d app -m 'hello world'\n" +
			"  tail -f access.log | bronto send -d app --collection prod\n" +
			"  echo '{\"message\":\"m\",\"level\":\"warn\"}' | bronto send -d app",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validatePositive("batch-size", batchSize); err != nil {
				return err
			}
			if err := validatePositive("batch-bytes", batchBytes); err != nil {
				return err
			}
			if flushInterval < 100*time.Millisecond {
				return clierr.New("usage_invalid_interval", "flush-interval must be at least 100ms")
			}
			tagStr, err := joinTags(tags)
			if err != nil {
				return err
			}

			app, err := NewApp(cmd)
			if err != nil {
				return err
			}

			url := ingestURLFlag
			if url == "" {
				if v, ok := app.Config.Get("ingest_url"); ok {
					url = v.Val
				}
			}
			if url == "" {
				v, _ := app.Config.Get("region") // config always has a "region" default ("eu")
				url = ingest.URL(v.Val, "")
			}

			sender := &ingest.Sender{
				HTTP:       app.HTTPClient,
				URL:        url,
				Dataset:    dataset,
				Collection: collection,
				Tags:       tagStr,
				Gzip:       !noGzip,
			}

			if cmd.Flags().Changed("message") {
				ev := ingest.LineToEvent(message, nil)
				if err := sender.Send(cmd.Context(), []map[string]any{ev}); err != nil {
					return err
				}
				if !app.Quiet {
					_, _ = fmt.Fprintln(app.Stderr, "Sent 1 event.")
				}
				return nil
			}

			return runSendStream(cmd, app, sender, batchSize, batchBytes, flushInterval)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&dataset, "dataset", "d", "", "destination dataset (required)")
	f.StringVar(&collection, "collection", "", "destination collection")
	f.StringArrayVar(&tags, "tag", nil, "tag as key=value (repeatable)")
	f.StringVarP(&message, "message", "m", "", "send exactly one event with this message and exit (ignores stdin)")
	f.StringVar(&ingestURLFlag, "ingest-url", "", "override the ingestion URL (config: ingest_url, env: BRONTO_INGEST_URL)")
	f.IntVar(&batchSize, "batch-size", 500, "max events per batch")
	f.IntVar(&batchBytes, "batch-bytes", 1<<20, "max bytes per batch")
	f.DurationVar(&flushInterval, "flush-interval", time.Second, "flush a partial batch at least this often (min 100ms)")
	f.BoolVar(&noGzip, "no-gzip", false, "disable gzip compression of the request body")
	_ = cmd.MarkFlagRequired("dataset")
	return cmd
}

// joinTags validates each "k=v" tag and joins them with commas for the
// x-bronto-tags header.
func joinTags(tags []string) (string, error) {
	for _, t := range tags {
		if !strings.Contains(t, "=") {
			return "", clierr.New("usage_invalid_tag",
				fmt.Sprintf("--tag %q must be in key=value form", t))
		}
	}
	return strings.Join(tags, ","), nil
}

// runSendStream reads NDJSON/plain-text lines from stdin, batches them, and
// flushes on batch-full, a flush-interval ticker, EOF, or context
// cancellation (best-effort final flush, then a clean exit).
func runSendStream(cmd *cobra.Command, app *App, sender *ingest.Sender, batchSize, batchBytes int, flushInterval time.Duration) error {
	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	lines := make(chan string)
	stop := make(chan struct{})
	defer close(stop)
	var scanErr error
	go func() {
		defer close(lines)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-stop:
				return
			}
		}
		scanErr = scanner.Err() // nil on clean EOF
	}()

	batcher := &ingest.Batcher{MaxEvents: batchSize, MaxBytes: batchBytes}
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	var sentEvents, sentBatches int
	flush := func() error {
		if batcher.Len() == 0 {
			return nil
		}
		events := batcher.Drain()
		if err := sender.Send(cmd.Context(), events); err != nil {
			return err
		}
		sentEvents += len(events)
		sentBatches++
		return nil
	}

	summary := func() {
		if !app.Quiet {
			_, _ = fmt.Fprintf(app.Stderr, "Sent %d event(s) in %d batch(es).\n", sentEvents, sentBatches)
		}
	}

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				// EOF or scanner read error: final flush first.
				if err := flush(); err != nil {
					return err
				}
				// Check if the scanner hit an error (e.g., line > 1 MiB buffer).
				if scanErr != nil {
					if !app.Quiet {
						_, _ = fmt.Fprintf(app.Stderr, "Sent %d event(s) in %d batch(es) before the input error.\n", sentEvents, sentBatches)
					}
					return clierr.New("ingest_read_error", fmt.Sprintf("reading input failed: %v", scanErr)).
						WithHint("Lines longer than 1 MiB cannot be sent; the events flushed before the error were delivered.")
				}
				summary()
				return nil
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			ev := ingest.LineToEvent(line, nil)
			if batcher.Add(ev) {
				if err := flush(); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if err := flush(); err != nil {
				return err
			}
		case <-cmd.Context().Done():
			_ = flush() // best-effort; ignore errors on shutdown
			summary()
			return nil
		}
	}
}
