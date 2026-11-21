package pipeline

import "testing"

type fakeCredentialAdapter struct {
	accounts []map[string]any
}

func (f fakeCredentialAdapter) GetAllAccountInfo() []map[string]any { return f.accounts }

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// stage snapshots the adapter's account list and the generated tool→provider
// map for prompt injection.
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
