package integrations

// GitHub's upstream tool wraps every successful call in
// {"success": true, "data": <provider body>}; get_user_emails is the one
// action that projects its own document (see githubUserEmails).
func githubShapes() map[string]shapeFunc {
	shapes := map[string]shapeFunc{}
	for _, action := range []string{
		"github_get_repo",
		"github_search_repos",
		"github_list_issues",
		"github_get_issue",
		"github_create_issue",
		"github_update_issue",
		"github_list_pull_requests",
		"github_get_pull_request",
		"github_create_pull_request",
		"github_search_code",
		"github_list_branches",
		"github_get_branch",
		"github_list_stargazers",
		"github_get_user_profile",
		"github_list_commits",
		"github_create_release",
	} {
		shapes[action] = githubSuccess
	}
	return shapes
}

// githubSuccess is the upstream {"success":true,"data":...} envelope. An
// unparseable body reads as an empty object, like _handle_response's
// exception branch; a provider error value is returned unchanged.
func githubSuccess(parsed any, _ map[string]any) any {
	if parsed == nil {
		parsed = map[string]any{}
	}
	if object, ok := parsed.(map[string]any); ok {
		if _, failed := object["error"]; failed {
			return parsed
		}
	}
	return map[string]any{"success": true, "data": parsed}
}

// githubEmailEntry is one address discovered from public commit activity.
type githubEmailEntry struct {
	Email  string
	Source string
}
