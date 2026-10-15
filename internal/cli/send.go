package cli

import (
	"bufio"
	"context"
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
		Short: "Send events to Bronto from a message or stdin",
		Long: "Sends events to Bronto's ingestion API. With -m/--message it sends one event\n" +
			"and exits. Otherwise it reads NDJSON or plain-text lines from stdin and sends them\n" +
			"in batches capped by --batch-size and --batch-bytes. A partial batch is flushed\n" +
			"every --flush-interval, so 'tail -f access.log | bronto send -d app' delivers\n" +
			"lines soon after they are written. If a batch fails, the command stops and the\n" +
			"events in that batch are lost. Rerunning the command can send duplicates, because\n" +
			"batches are not deduplicated.",
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
			// -d prod/app means dataset app in collection prod, as everywhere
			// else; sent verbatim, it created a dataset named "prod/app".
			if c, d, ok := strings.Cut(dataset, "/"); ok {
				if collection != "" {
					return clierr.New("usage_conflicting_flags",
						fmt.Sprintf("-d %q names a collection and --collection is set too", dataset)).
						WithHint("Use either -d <collection>/<dataset> or -d <dataset> --collection <collection>.")
				}
				if c == "" || d == "" || strings.Contains(d, "/") {
					return clierr.New("usage_invalid_dataset", fmt.Sprintf("-d %q is not <collection>/<dataset>", dataset))
				}
				collection, dataset = c, d
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
				if app.DryRun {
					p, perr := app.Printer(false)
					if perr != nil {
						return perr
					}
					return p.PrintJSON(map[string]any{
						"dry_run": true, "ingest_url": url, "dataset": dataset,
						"collection": collection, "events": []map[string]any{ev},
					})
				}
				if err := sender.Send(cmd.Context(), []map[string]any{ev}); err != nil {
					return err
				}
				if !app.Quiet {
					_, _ = fmt.Fprintln(app.Stderr, "Sent 1 event.")
				}
				return nil
			}

			if app.DryRun {
				return dryRunSendStream(cmd, app, url, dataset, collection)
			}
			return runSendStream(cmd, app, sender, batchSize, batchBytes, flushInterval)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&dataset, "dataset", "d", "", "destination dataset (required)")
	f.StringVar(&collection, "collection", "", "destination collection")
	f.StringArrayVar(&tags, "tag", nil, "tag to attach, as key=value (repeatable)")
	f.StringVarP(&message, "message", "m", "", "send one event with this message and exit (stdin is ignored)")
	f.StringVar(&ingestURLFlag, "ingest-url", "", "override the ingestion URL (config: ingest_url, env: BRONTO_INGEST_URL)")
	f.IntVar(&batchSize, "batch-size", 500, "max events per batch")
	f.IntVar(&batchBytes, "batch-bytes", 1<<20, "max bytes per batch")
	f.DurationVar(&flushInterval, "flush-interval", time.Second, "flush a partial batch at least this often (min 100ms)")
	f.BoolVar(&noGzip, "no-gzip", false, "send request bodies uncompressed")
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

// shutdownFlushTimeout bounds the final flush after Ctrl-C/SIGTERM: the
// signal context is already cancelled, so that flush runs on a detached
// context that gives the last batch this long to land.
const shutdownFlushTimeout = 5 * time.Second

// ShutdownError is a failure in the clean-up a command still runs after
// Ctrl-C/SIGTERM, such as send's final flush. main renders it and exits
// with its own code instead of the silent 130 of an interrupted run: the
// user has to know that events were lost.
type ShutdownError struct{ Err error }

func (e *ShutdownError) Error() string { return e.Err.Error() }
func (e *ShutdownError) Unwrap() error { return e.Err }

// runSendStream reads NDJSON/plain-text lines from stdin, batches them, and
// flushes on batch-full, a flush-interval ticker, EOF, or context
// cancellation. On cancellation the pending batch is flushed on a detached
// context; a failed flush is reported, and a successful one still returns
// the cancellation error so the process exits 130.
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
	send := func(ctx context.Context, events []map[string]any) error {
		if err := sender.Send(ctx, events); err != nil {
			return err
		}
		sentEvents += len(events)
		sentBatches++
		return nil
	}
	// flush sends the pending batch. Once the command context is cancelled
	// (Ctrl-C/SIGTERM), it sends on a detached context bounded by
	// shutdownFlushTimeout instead, also retrying a batch whose request
	// the interrupt cut off (which can duplicate it if it had landed).
	flush := func() error {
		if batcher.Len() == 0 {
			return nil
		}
		events := batcher.Drain()
		if cmd.Context().Err() == nil {
			err := send(cmd.Context(), events)
			if err == nil || cmd.Context().Err() == nil {
				return err
			}
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), shutdownFlushTimeout)
		defer cancel()
		return send(ctx, events)
	}
	// fail reports a flush error. After an interrupt it is a ShutdownError,
	// so main reports it instead of exiting with a silent 130.
	fail := func(err error) error {
		if cmd.Context().Err() == nil {
			return err
		}
		if !app.Quiet {
			_, _ = fmt.Fprintf(app.Stderr, "Sent %d event(s) in %d batch(es) before the interrupt; the last batch was not delivered.\n", sentEvents, sentBatches)
		}
		return &ShutdownError{Err: err}
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
					return fail(err)
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
				// nil on a normal EOF; the cancellation (exit 130) when an
				// interrupt arrived during the final flush.
				return cmd.Context().Err()
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			ev := ingest.LineToEvent(line, nil)
			if batcher.Add(ev) {
				if err := flush(); err != nil {
					return fail(err)
				}
			}
		case <-ticker.C:
			if err := flush(); err != nil {
				return fail(err)
			}
		case <-cmd.Context().Done():
			if err := flush(); err != nil {
				return fail(err)
			}
			summary()
			// The cancellation error makes main exit 130 without an error message.
			return cmd.Context().Err()
		}
	}
}

// dryRunSendStream consumes stdin like the real stream path would, but
// only counts events/bytes and prints a plan instead of posting anything.
func dryRunSendStream(cmd *cobra.Command, app *App, url, dataset, collection string) error {
	sc := bufio.NewScanner(cmd.InOrStdin())
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	events, bytes := 0, 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		events++
		bytes += len(line)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	p, err := app.Printer(false)
	if err != nil {
		return err
	}
	return p.PrintJSON(map[string]any{
		"dry_run": true, "ingest_url": url, "dataset": dataset,
		"collection": collection, "events": events, "bytes": bytes,
	})
}
