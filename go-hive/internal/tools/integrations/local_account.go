package integrations

import (
	"context"
	"strings"
)

// accountInfoSource adapts a credential store view to the upstream shape:
// {"accounts": [...], "count": n}, filtered by provider when supplied.
func getAccountInfo(_ context.Context, rt *Runtime, input map[string]any) (any, error) {
	provider := strings.TrimSpace(stringArg(input, "provider"))
	if rt.Accounts == nil {
		return map[string]any{"accounts": []any{}, "message": "No credential store configured"}, nil
	}
	accounts := rt.Accounts.Accounts(provider)
	if accounts == nil {
		accounts = []map[string]any{}
	}
	return map[string]any{"accounts": accounts, "count": len(accounts)}, nil
}
