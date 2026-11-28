package pipeline

import (
	"strings"
	"testing"
)

type fakeCredentialAdapter struct {
	accounts []map[string]any
}

func (f fakeCredentialAdapter) GetAllAccountInfo() []map[string]any { return f.accounts }

// Parity: core/framework/orchestrator/prompting.py::build_accounts_prompt
// (heading, per-provider header, alias-first lines, multi-account note)
func TestBuildAccountsPromptMatchesUpstreamFormat(t *testing.T) {
	accounts := []map[string]any{
		{"provider": "github", "alias": "work", "identity": map[string]any{"login": "octocat"}},
		{"provider": "github", "alias": "personal", "source": "local"},
		{"provider": "notion", "alias": "team", "identity": map[string]any{"email": "t@example.com"}},
	}
	toolProviderMap := map[string]string{
		"github_get_issue":    "github",
		"github_create_issue": "github",
		"notion_create_page":  "notion",
		"slack_post_message":  "slack",
	}
	want := "# Connected integrations\n" +
		"\n" +
		"github (use account=\"<alias>\" with: github_create_issue, github_get_issue)\n" +
		"- \"work\" (octocat)\n" +
		"- \"personal\" [local]\n" +
		"\n" +
		"notion (use account=\"<alias>\" with: notion_create_page)\n" +
		"- \"team\" (t@example.com)\n" +
		"\n" +
		"When a provider below has multiple accounts, ask the user which one to use and list the options — do not guess."
	if got := buildAccountsPrompt(accounts, toolProviderMap, nil); got != want {
		t.Fatalf("prompt mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Parity: core/framework/orchestrator/prompting.py::build_accounts_prompt
// (node_tool_names filters a provider out entirely when none of its tools are
// on the node)
func TestBuildAccountsPromptFiltersByNodeTools(t *testing.T) {
	accounts := []map[string]any{
		{"provider": "github", "alias": "work"},
		{"provider": "notion", "alias": "team"},
	}
	toolProviderMap := map[string]string{"github_get_issue": "github", "notion_create_page": "notion"}
	got := buildAccountsPrompt(accounts, toolProviderMap, []string{"notion_create_page"})
	if strings.Contains(got, "github") {
		t.Fatalf("github block must be filtered out: %q", got)
	}
	if !strings.Contains(got, "notion (use account=\"<alias>\" with: notion_create_page)") {
		t.Fatalf("notion block missing: %q", got)
	}
}

// Parity: core/framework/orchestrator/prompting.py::build_accounts_prompt
// (empty accounts render nothing)
func TestBuildAccountsPromptEmptyAccounts(t *testing.T) {
	if got := buildAccountsPrompt(nil, map[string]string{"github_get_issue": "github"}, nil); got != "" {
		t.Fatalf("prompt = %q, want empty", got)
	}
}

// Parity: framework/pipeline/stages/credential_resolver.py::CredentialResolverStage.initialize
// The stage snapshots the adapter's accounts, the generated tool→provider map,
// and renders the connected-accounts prompt block.
func TestCredentialResolverSnapshotsAccounts(t *testing.T) {
	stage := &CredentialResolverStage{
		BaseStage: BaseStage{StageName: "credential_resolver", StageOrder: 40},
		Adapter: fakeCredentialAdapter{accounts: []map[string]any{
			{"provider": "github", "alias": "work"},
		}},
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if len(stage.AccountsData) != 1 || stage.AccountsData[0]["alias"] != "work" {
		t.Fatalf("accounts = %v", stage.AccountsData)
	}
	if len(stage.ToolProviderMap) == 0 {
		t.Fatal("tool provider map not populated")
	}
	if !strings.HasPrefix(stage.AccountsPrompt, "# Connected integrations\n") {
		t.Fatalf("accounts prompt = %q, want the upstream heading", stage.AccountsPrompt)
	}
	if !strings.Contains(stage.AccountsPrompt, "github (use account=\"<alias>\" with: ") {
		t.Fatalf("accounts prompt missing github tool header: %q", stage.AccountsPrompt)
	}
	if !strings.Contains(stage.AccountsPrompt, "- \"work\"") {
		t.Fatalf("accounts prompt missing alias-first line: %q", stage.AccountsPrompt)
	}
}

// Parity: framework/pipeline/stages/credential_resolver.py::CredentialResolverStage.initialize
// No accounts leaves the prompt empty (upstream only renders when accounts_data
// is truthy).
func TestCredentialResolverNoAccountsLeavesPromptEmpty(t *testing.T) {
	stage := &CredentialResolverStage{
		BaseStage: BaseStage{StageName: "credential_resolver", StageOrder: 40},
		Adapter:   fakeCredentialAdapter{},
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if stage.AccountsPrompt != "" {
		t.Fatalf("accounts prompt = %q, want empty", stage.AccountsPrompt)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// default adapter path is non-fatal on an empty home (no accounts, no error).
func TestCredentialResolverDefaultAdapterIsNonFatal(t *testing.T) {
	testHome(t)
	stage := &CredentialResolverStage{
		BaseStage: BaseStage{StageName: "credential_resolver", StageOrder: 40},
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize must swallow adapter failures: %v", err)
	}
	if len(stage.AccountsData) != 0 {
		t.Fatalf("accounts = %v, want none", stage.AccountsData)
	}
	if stage.ToolProviderMap == nil {
		t.Fatal("tool provider map must be installed even with no accounts")
	}
	if stage.AccountsPrompt != "" {
		t.Fatalf("accounts prompt = %q, want empty", stage.AccountsPrompt)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; Process
// is a pass-through for infrastructure stages.
func TestCredentialResolverProcessPassesThrough(t *testing.T) {
	stage := &CredentialResolverStage{
		BaseStage: BaseStage{StageName: "credential_resolver", StageOrder: 40},
	}
	result, err := stage.Process(NewContext("api", map[string]any{}))
	if err != nil || result.Action != ActionContinue {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
