// Command signupmock serves the fake sign-up backend (package fake) for
// the 'bronto signup' prototype recordings: docs/tapes/signup/render.sh.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/bronto-community/bronto-cli/internal/tools/signupmock/fake"
)

func main() {
	urlFile := flag.String("url-file", "", "write the base URL to this file once listening")
	flag.Parse()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	if *urlFile != "" {
		if err := os.WriteFile(*urlFile, []byte(url+"\n"), 0o600); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("signupmock: listening on %s", url)
	hs := &http.Server{Handler: fake.New(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(hs.Serve(ln))
}
