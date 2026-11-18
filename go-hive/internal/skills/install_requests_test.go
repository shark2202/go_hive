package skills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testInstallDraft(name, body string) *SkillDraft {
	if name == "" {
		name = "house-style"
	}
	if body == "" {
		body = "# House style\n\nLead with the answer.\n"
	}
	draft, errMsg := BuildSkillDraft(name, "How Ava formats her reports.", body, nil)
	if draft == nil {
		panic(errMsg)
	}
	return draft
}

// Parity: tests/test_skill_install_requests.py::test_a_new_request_replaces_the_pending_one_for_the_same_name
func TestNewRequestReplacesPendingForSameName(t *testing.T) {
	dir := t.TempDir()
	first, err := AddRequest(dir, testInstallDraft("", ""))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	other, err := AddRequest(dir, testInstallDraft("other-skill", ""))
	if err != nil {
		t.Fatalf("other: %v", err)
	}
	second, err := AddRequest(dir, testInstallDraft("", "# House style\n\nShorter.\n"))
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	pending := PendingRequests(dir)
	if len(pending) != 2 || pending[0].ID != other.ID || pending[1].ID != second.ID {
		t.Fatalf("pending ids = %+v", pending)
	}
	if first.ID == second.ID {
		t.Fatal("replacement should get a fresh id")
	}
}

// Parity: tests/test_skill_install_requests.py::test_a_decided_request_is_no_longer_pending
func TestDecidedRequestIsNoLongerPending(t *testing.T) {
	dir := t.TempDir()
	request, err := AddRequest(dir, testInstallDraft("", ""))
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := MarkDecided(dir, request.ID, InstallStatusDenied); err != nil {
		t.Fatalf("mark: %v", err)
	}

	if pending := PendingRequests(dir); len(pending) != 0 {
		t.Fatalf("pending = %+v", pending)
	}
	var decided *RequestDecided
	if _, err := PendingRequest(dir, request.ID); !errors.As(err, &decided) {
		t.Fatalf("err = %v, want RequestDecided", err)
	} else if !strings.Contains(err.Error(), "already denied") {
		t.Fatalf("error = %q", err.Error())
	}
}

// Parity: tests/test_skill_install_requests.py::test_an_unknown_request_is_not_found
func TestUnknownRequestIsNotFound(t *testing.T) {
	var notFound *RequestNotFound
	if _, err := PendingRequest(t.TempDir(), "nope"); !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want RequestNotFound", err)
	} else if !strings.Contains(err.Error(), "no skill install request nope") {
		t.Fatalf("error = %q", err.Error())
	}
}

// Parity: tests/test_skill_install_requests.py::test_the_stored_copy_is_what_gets_installed
func TestStoredCopyIsWhatGetsInstalled(t *testing.T) {
	dir := t.TempDir()
	request, err := AddRequest(dir, testInstallDraft("", ""))
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	pending, err := PendingRequest(dir, request.ID)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	draft, err := DraftOf(pending)
	if err != nil {
		t.Fatalf("draft: %v", err)
	}

	if draft.Name != "house-style" || draft.Body != "# House style\n\nLead with the answer.\n" {
		t.Fatalf("draft = (%q, %q)", draft.Name, draft.Body)
	}
}

// Parity-exempt: the install-request store JSON shape has no Python pytest.
func TestInstallRequestsStoreJSONShape(t *testing.T) {
	dir := t.TempDir()
	request, err := AddRequest(dir, testInstallDraft("", ""))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, InstallRequestsFileName))
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`"id": "` + request.ID + `"`,
		`"name": "house-style"`,
		`"requested_at": "`,
		`"status": "pending"`,
		`"files": []`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("store missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "decided_at") {
		t.Fatalf("pending request should omit decided_at:\n%s", text)
	}
}

// Parity-exempt: permissive loading of a corrupt install-request store has no
func TestInstallRequestsLoadIsPermissive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, InstallRequestsFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if pending := PendingRequests(dir); len(pending) != 0 {
		t.Fatalf("malformed store should be empty, got %+v", pending)
	}

	body := `[
	  {"id": "keep-me", "name": "a", "status": "pending"},
	  {"name": "no-id"},
	  7
	]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pending := PendingRequests(dir)
	if len(pending) != 1 || pending[0].ID != "keep-me" {
		t.Fatalf("pending = %+v", pending)
	}
}
