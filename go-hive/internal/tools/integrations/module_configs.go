package integrations

import "strings"

// moduleConfigs declares credentials and wire conventions per module for the
// catalog-driven core tools (batch 1). Batch-specific modules declare their
// own maps next to their registries.
func moduleConfigs() map[string]*ModuleConfig {
	bearer := func(token string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + token}
	}
	return map[string]*ModuleConfig{
		"stripe_tool": {
			ID: "stripe_tool", BaseURL: "https://api.stripe.com", EnvVars: []string{"STRIPE_API_KEY"},
			Headers: bearer, Form: true,
		},
		"slack_tool": {
			ID: "slack_tool", BaseURL: "https://slack.com/api", EnvVars: []string{"SLACK_BOT_TOKEN"},
			Headers: bearer,
		},
		"github_tool": {
			ID: "github_tool", BaseURL: "https://api.github.com", EnvVars: []string{"GITHUB_TOKEN"},
			Headers: func(token string) map[string]string {
				return map[string]string{
					"Authorization":        "Bearer " + token,
					"Accept":               "application/vnd.github+json",
					"X-GitHub-Api-Version": "2022-11-28",
				}
			},
		},
		"telegram_tool": {
			ID: "telegram_tool", BaseURL: "https://api.telegram.org/bot{token}", EnvVars: []string{"TELEGRAM_BOT_TOKEN"},
		},
		"trello_tool": {
			ID: "trello_tool", BaseURL: "https://api.trello.com/1", EnvVars: []string{"TRELLO_API_KEY", "TRELLO_API_TOKEN"},
			Form: true,
			ResolveToken: func(getenv func(string) string) (string, []string) {
				key, token := getenv("TRELLO_API_KEY"), getenv("TRELLO_API_TOKEN")
				if key == "" || token == "" {
					var missing []string
					if key == "" {
						missing = append(missing, "TRELLO_API_KEY")
					}
					if token == "" {
						missing = append(missing, "TRELLO_API_TOKEN")
					}
					return "", missing
				}
				return key + "|" + token, nil
			},
			Query: func(token string) map[string]string {
				parts := strings.SplitN(token, "|", 2)
				if len(parts) != 2 {
					return nil
				}
				return map[string]string{"key": parts[0], "token": parts[1]}
			},
		},
		"airtable_tool": {
			ID: "airtable_tool", BaseURL: "https://api.airtable.com/v0", EnvVars: []string{"AIRTABLE_PAT"},
			Headers: bearer,
		},
	}
}
