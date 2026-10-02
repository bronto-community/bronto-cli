// Command docsmock runs the deterministic docs mock server (package
// internal/tools/docsmock/mock) as a standalone process, for VHS tapes
// and for poking at the docs world by hand:
//
//	go run ./internal/tools/docsmock -addr 127.0.0.1:0 -print-url
//
// With -print-url it prints the base URL (e.g. http://127.0.0.1:53211) on
// stdout once it is listening. Point the CLI at it with:
//
//	BRONTO_BASE_URL=<url>
//	BRONTO_INGEST_URL=<url>/ingest
//	BRONTO_API_KEY=bronto_docs_example_key
//
// plus a throwaway BRONTO_CONFIG_DIR (see docs/tapes/record.sh).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bronto-community/bronto-cli/internal/tools/docsmock/mock"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address (port 0 picks a free port)")
	dir := flag.String("dir", "", "fixture directory (default: docs/testdata/mock in the enclosing repo)")
	printURL := flag.Bool("print-url", false, "print the base URL on stdout once listening")
	urlFile := flag.String("url-file", "", "also write the base URL to this file once listening")
	flag.Parse()

	if *dir == "" {
		d, err := findFixtures()
		if err != nil {
			log.Fatal(err)
		}
		*dir = d
	}
	srv, err := mock.New(*dir)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	if *printURL {
		fmt.Println(url)
	}
	if *urlFile != "" {
		if err := os.WriteFile(*urlFile, []byte(url+"\n"), 0o600); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("docsmock: serving %s on %s", *dir, url)

	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdownCtx)
	}()
	err = hs.Serve(ln)
	stop()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err) //nolint:gocritic // stop() already ran
	}
}

// findFixtures walks up from the working directory to the repo root (the
// directory holding go.mod) and returns its docs/testdata/mock.
func findFixtures() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return filepath.Join(d, "docs", "testdata", "mock"), nil
		}
		if filepath.Dir(d) == d {
			return "", errors.New("docsmock: no go.mod above the working directory; pass -dir")
		}
	}
}
