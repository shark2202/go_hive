package pipeline

import (
	"log"

	"github.com/aden-hive/hive-go/internal/credentials"
)

// CredentialAdapter is the narrow view the stage needs of the credential
// store: the connected-account list.
type CredentialAdapter interface {
	GetAllAccountInfo() []map[string]any
}

// CredentialResolverStage resolves connected accounts at startup for system
// prompt injection. Individual credential TTL/refresh is handled by the tool
// boundary on every call, so the stage only snapshots accounts here.
//
// Failure is non-fatal, exactly as upstream: a broken credential store logs
// and leaves the outputs empty rather than failing startup.
//
// Config keys (config.*): credential_store is a typed injection
// (struct field Adapter), not a JSON value; unknown keys are ignored upstream
// and here.
//
// Parity: framework/pipeline/stages/credential_resolver.py::CredentialResolverStage
type CredentialResolverStage struct {
	BaseStage
	// Adapter is the credential store view; nil builds the default adapter.
	Adapter CredentialAdapter
	// AccountsData is the resolved account snapshot (output).
	AccountsData []map[string]any
	// ToolProviderMap maps credential-backed tool names to their credential
	// id (output; the generated table the adapter portal exposes).
	ToolProviderMap map[string]string
	// AccountsPrompt is the connected-accounts prompt block. The Go port
	// keeps it empty: the prompt text is assembled by the queen prompt
	// builder (queen.CredentialsSummaryBlock), not duplicated here.
	AccountsPrompt string
}

// NewCredentialResolverStage builds the stage from its declarative config.
func NewCredentialResolverStage(spec map[string]any) (Stage, error) {
	_ = configMap(spec)
	return &CredentialResolverStage{
		BaseStage: BaseStage{StageName: "credential_resolver", StageOrder: 40},
	}, nil
}

// Initialize snapshots the connected accounts; failures are logged and
// swallowed (upstream's non-fatal resolution).
//
// Parity: framework/pipeline/stages/credential_resolver.py::CredentialResolverStage.initialize
func (s *CredentialResolverStage) Initialize() error {
	adapter := s.Adapter
	if adapter == nil {
		// CatalogNamedSpecs is the NamedSpec projection of the upstream
		// CREDENTIAL_SPECS table — what CredentialStoreAdapter.default()
		// indexes accounts by.
		defaultAdapter, err := credentials.DefaultAdapter(credentials.CatalogNamedSpecs())
		if err != nil {
			log.Printf("[pipeline] CredentialResolverStage: %v (non-fatal)", err)
			return nil
		}
		adapter = defaultAdapter
	}
	s.AccountsData = adapter.GetAllAccountInfo()
	s.ToolProviderMap = credentials.ToolProviderMap()
	return nil
}

// Process is a pass-through: the work happened in Initialize.
func (s *CredentialResolverStage) Process(_ *Context) (Result, error) { return ContinueResult(), nil }
