// Command memgw serves the deterministic OpenAI-compatible gateway the memory
// end-to-end runs point their LLM config at. It carries no product code: a
// run starts it beside `hive serve` (either implementation) and reads the
// recorded prompts from the log file.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/shark2202/go_hive/go-hive/e2e/memgw"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address (0 picks a free port)")
	logPath := flag.String("log", "", "append one JSON record per request to this file")
	flag.Parse()

	server, err := memgw.New(memgw.Options{LogPath: *logPath})
	if err != nil {
		fmt.Fprintf(os.Stderr, "memgw: %v\n", err)
		os.Exit(1)
	}
	defer server.Close()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "memgw: listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("LISTENING http://%s/v1\n", listener.Addr().String())
	_ = http.Serve(listener, server.Handler())
}
