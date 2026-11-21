// Command stallgw serves the deterministic OpenAI-compatible gateway the
// stall-check end-to-end runs point their LLM config at. It carries no product
// code: a run starts it beside `hive serve` (either implementation) and reads
// the recorded requests from the log file.
//
// Unlike the in-test httptest server, this binary lets a cross-implementation
// run keep the queen model scripted while the judge slot goes to a real
// endpoint: only `POST /judge/systemone` would be answered here, and runs that
// exercise a real judge never route it this way.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/aden-hive/hive-go/e2e/stallgw"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address (0 picks a free port)")
	logPath := flag.String("log", "", "append one JSON record per request to this file")
	model := flag.String("model", "stall-e2e-model", "model name echoed in completions")
	announcement := flag.String("announcement", "", "first queen turn's reply (no tool call)")
	later := flag.String("later", "", "every queen turn after the first")
	title := flag.String("title", "", "answer to session-title requests")
	toolCall := flag.String("tool-call", "", "first queen turn calls this tool instead of announcing")
	toolReply := flag.String("tool-reply", "", "queen turn that follows the tool result")
	probability := flag.Float64("probability", 0.95, "judge noul answer for /judge/systemone")
	replyWhen := flag.String("reply-when", "", "only requests carrying this marker advance the scripted turns")
	filler := flag.String("filler", "", "reply for queen calls without the -reply-when marker")
	flag.Parse()

	server := stallgw.New(stallgw.Options{
		Model:            *model,
		Announcement:     *announcement,
		LaterReply:       *later,
		Title:            *title,
		ToolCallName:     *toolCall,
		ToolCallReply:    *toolReply,
		StallProbability: *probability,
		LogPath:          *logPath,
		ReplyWhen:        *replyWhen,
		FillerReply:      *filler,
	})

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stallgw: listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("LISTENING http://%s/v1\n", listener.Addr().String())
	_ = http.Serve(listener, server.Handler())
}
