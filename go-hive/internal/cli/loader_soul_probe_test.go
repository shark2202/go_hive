package cli

import (
	"net/http/httptest"
	"testing"

	"github.com/shark2202/go_hive/go-hive/e2e/soulgw"
)

// Parity: framework/agents/queen/soul_reflection.py::llm_for
//
// A probe is answered on the model the daily reflection would use
// (routes_soul.py passes llm_for), not on the main chat slot: worker_llm when
// her soul is set to worker and that slot is configured.
func TestSoulProbeAnswerUsesTheWorkerSlot(t *testing.T) {
	mainGateway := soulgw.New(soulgw.Options{Model: "main-e2e-model"})
	mainServer := httptest.NewServer(mainGateway.Handler())
	defer mainServer.Close()
	workerGateway := soulgw.New(soulgw.Options{Model: "worker-e2e-model"})
	workerServer := httptest.NewServer(workerGateway.Handler())
	defer workerServer.Close()

	home := soulReflectTestHome(t, mainServer.URL, "review")
	soulUseWorkerSlot(t, home, workerServer.URL)

	answerFor, err := newSoulProbeAnswerFor(home, "")(soulReflectTestQueen, soulReflectTestConfig(t, home))
	if err != nil {
		t.Fatalf("resolve probe model: %v", err)
	}
	answer, err := answerFor("IDENTITY", "[Ann(12345)]: are you well?")
	if err != nil {
		t.Fatalf("answer probe: %v", err)
	}
	if answer == "" {
		t.Fatal("the worker slot returned no probe answer")
	}
	if requests := workerGateway.Requests(); len(requests) != 1 {
		t.Fatalf("worker requests = %d, want the probe answer", len(requests))
	}
	if requests := mainGateway.Requests(); len(requests) != 0 {
		t.Fatalf("the main chat slot answered %d requests, want the worker slot only", len(requests))
	}
}
