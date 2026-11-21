// Command recordproxy runs the pass-through recording proxy used by the
// real-model E2E runs (Go test and the Python dual-run driver). It forwards to
// a real OpenAI-compatible upstream and appends one JSON record per exchange to
// a log file; the API key is read from the environment and never logged.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/aden-hive/hive-go/e2e/recordproxy"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address (0 picks a free port)")
	upstream := flag.String("upstream", "", "real upstream OpenAI-compatible base URL, e.g. https://host/v1")
	logPath := flag.String("log", "", "append one JSON record per exchange to this file")
	keyEnv := flag.String("key-env", "BROADTOKEN_API_KEY", "environment variable holding the real upstream API key")
	bodyHead := flag.Int("body-head", 0, "recorded request body head limit in bytes (0 = default)")
	bodyTail := flag.Int("body-tail", 0, "recorded request body tail limit in bytes (0 = default)")
	flag.Parse()

	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "recordproxy: -upstream is required")
		os.Exit(2)
	}
	server, err := recordproxy.New(recordproxy.Options{
		Upstream:      *upstream,
		APIKey:        os.Getenv(*keyEnv),
		LogPath:       *logPath,
		BodyHeadLimit: *bodyHead,
		BodyTailLimit: *bodyTail,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "recordproxy: %v\n", err)
		os.Exit(1)
	}
	defer server.Close()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recordproxy: listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("LISTENING http://%s/v1\n", listener.Addr().String())
	_ = http.Serve(listener, server.Handler())
}
