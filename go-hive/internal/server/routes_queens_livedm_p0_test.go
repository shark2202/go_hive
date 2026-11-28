package server

import (
	"net/http"
	"testing"
)

// Parity: framework/server/routes_queens.py::handle_queen_session
// Parity: framework/server/routes_queens.py::handle_new_queen_session
//
// A channel's session is the channel's conversation, not the user's DM, and a
// session retired by a fork is no longer live. Neither may be served as the
// queen's live DM by POST .../session nor parked by POST .../session/new.
func TestQueenLiveDMExcludesChannelAndSupersededSessions(t *testing.T) {
	dm := &Session{ID: "dm", QueenName: "queen_technology"}
	channel := &Session{ID: "channel", QueenName: "queen_technology"}
	channel.SetChannel("slack")
	superseded := &Session{ID: "superseded", QueenName: "queen_technology", SupersededBy: "dm"}
	colony := &Session{ID: "colony", QueenName: "queen_technology", ColonyID: "atlas"}
	worker := &Session{ID: "worker", QueenName: "queen_technology", WorkerPath: "agents/worker"}

	cases := []struct {
		name    string
		session *Session
		want    bool
	}{
		{"plain DM", dm, true},
		{"channel session", channel, false},
		{"superseded session", superseded, false},
		{"colony session", colony, false},
		{"worker session", worker, false},
		{"nil session", nil, false},
	}
	for _, testCase := range cases {
		if got := queenLiveDM(testCase.session, "queen_technology"); got != testCase.want {
			t.Errorf("%s: queenLiveDM = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// Parity: framework/server/routes_queens.py::handle_queen_session
//
// With only channel/superseded sessions live, the route must not short-circuit
// on them: it mints a fresh DM instead of serving someone else's thread.
func TestQueenSessionDoesNotServeChannelOrSupersededSessionsAsLiveDM(t *testing.T) {
	home := queenTestHome(t)
	manager := NewSessionManager()
	channel := &Session{ID: "channel_seen", QueenName: "queen_technology", LoadedAt: 9}
	channel.SetChannel("slack")
	manager.AddSession(channel)
	manager.AddSession(&Session{ID: "retired", QueenName: "queen_technology", LoadedAt: 8, SupersededBy: "fresh"})

	create := &queenCreateRecorder{}
	mux := queenTestMux(home, manager, func(deps *QueenRouteDeps) { deps.CreateSession = create.hook })
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["status"] != "created" {
		t.Fatalf("body = %#v, want a fresh created DM", body)
	}
	if len(create.calls) != 1 {
		t.Fatalf("create calls = %d, want 1", len(create.calls))
	}
}

// Parity: framework/server/routes_queens.py::handle_new_queen_session
//
// park_others only parks the queen's own DMs: a channel's session keeps
// serving its channel after the user clicks New Session.
func TestNewQueenSessionParksDMsButNotChannelSessions(t *testing.T) {
	home := queenTestHome(t)
	manager := NewSessionManager()
	dm := &Session{ID: "prior_dm", QueenName: "queen_technology"}
	channel := &Session{ID: "channel_seen", QueenName: "queen_technology"}
	channel.SetChannel("slack")
	manager.AddSession(dm)
	manager.AddSession(channel)

	create := &queenCreateRecorder{}
	mux := queenTestMux(home, manager, func(deps *QueenRouteDeps) { deps.CreateSession = create.hook })
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_technology/session/new", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["status"] != "created" {
		t.Fatalf("body = %#v", body)
	}
	if manager.GetSession("prior_dm") != nil {
		t.Fatal("the queen's prior DM was not parked")
	}
	if manager.GetSession("channel_seen") == nil {
		t.Fatal("a channel session was parked by /session/new")
	}
}
