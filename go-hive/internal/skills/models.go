package skills

type SkillScope string

const (
	ScopeProject   SkillScope = "project"
	ScopeUser      SkillScope = "user"
	ScopeFramework SkillScope = "framework"
)

type TrustStatus string

const (
	TrustStatusTrusted        TrustStatus = "trusted"
	TrustStatusPendingConsent TrustStatus = "pending_consent"
	TrustStatusDenied         TrustStatus = "denied"
)

type SkillEntry struct {
	Name        string
	Description string
	Location    string
	BaseDir     string
	SourceScope SkillScope
	TrustStatus TrustStatus

	License       *string
	Compatibility []string
	AllowedTools  []string
	Metadata      map[string]any
}
