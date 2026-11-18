package credentials

import (
	"errors"
	"testing"
)

func testResolverStore() *CredentialStore {
	return ForTesting(map[string]map[string]string{
		"brave_search": {"api_key": "test-brave-key"},
		"github_oauth": {"access_token": "ghp_xxx", "refresh_token": "ghr_xxx"},
		"empty":        {},
	})
}

// Parity: core/framework/credentials/tests/test_credential_store.py::TestTemplateResolver (simple, multiple, default key, headers,
func TestTemplateResolver(t *testing.T) {
	store := testResolverStore()
	resolver := NewTemplateResolver(store)

	resolved, err := resolver.Resolve("Bearer {{github_oauth.access_token}}")
	if err != nil || resolved != "Bearer ghp_xxx" {
		t.Fatalf("simple = %q %v", resolved, err)
	}

	resolved, err = resolver.Resolve("{{github_oauth.access_token}} and {{brave_search.api_key}}")
	if err != nil || resolved != "ghp_xxx and test-brave-key" {
		t.Fatalf("multiple = %q %v", resolved, err)
	}

	resolved, err = resolver.Resolve("Key: {{brave_search}}")
	if err != nil || resolved != "Key: test-brave-key" {
		t.Fatalf("default key = %q %v", resolved, err)
	}

	headers, err := resolver.ResolveHeaders(map[string]string{
		"Authorization": "Bearer {{github_oauth.access_token}}",
		"X-API-Key":     "{{brave_search.api_key}}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "Bearer ghp_xxx" || headers["X-API-Key"] != "test-brave-key" {
		t.Fatalf("headers = %v", headers)
	}

	_, err = resolver.Resolve("{{nonexistent.key}}")
	var notFound *CredentialNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("missing credential error = %T %v", err, err)
	}
	if err.Error() != "Credential 'nonexistent' not found" {
		t.Fatalf("message = %q", err)
	}

	_, err = resolver.Resolve("{{github_oauth.nonexistent}}")
	var keyNotFound *CredentialKeyNotFoundError
	if !errors.As(err, &keyNotFound) {
		t.Fatalf("missing key error = %T %v", err, err)
	}
	if err.Error() != "Key 'nonexistent' not found in credential 'github_oauth'" {
		t.Fatalf("message = %q", err)
	}

	if !resolver.HasTemplates("{{cred.key}}") || !resolver.HasTemplates("Bearer {{token}}") {
		t.Fatal("template detection failed")
	}
	if resolver.HasTemplates("no templates here") {
		t.Fatal("false positive template")
	}

	refs := resolver.ExtractReferences("{{github.token}} and {{brave.key}}")
	if len(refs) != 2 || refs[0].CredentialID != "github" || refs[0].Key == nil || *refs[0].Key != "token" {
		t.Fatalf("refs = %+v", refs)
	}
	if refs[1].CredentialID != "brave" || refs[1].Key == nil || *refs[1].Key != "key" {
		t.Fatalf("refs = %+v", refs)
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::test_resolve_missing_credential,
func TestTemplateResolverMissingModes(t *testing.T) {
	store := testResolverStore()
	resolver := NewTemplateResolver(store)

	resolved, err := resolver.ResolveWithMissing("A {{nope.key}} B", false)
	if err != nil || resolved != "A {{nope.key}} B" {
		t.Fatalf("fail_on_missing=false = %q %v", resolved, err)
	}

	_, err = resolver.Resolve("{{empty}}")
	var keyNotFound *CredentialKeyNotFoundError
	if !errors.As(err, &keyNotFound) || err.Error() != "Credential 'empty' has no keys" {
		t.Fatalf("empty credential error = %v", err)
	}

	problems, err := resolver.ValidateReferences("{{github_oauth.nonexistent}} {{missing.key}} {{empty}}")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 3 {
		t.Fatalf("problems = %v", problems)
	}
	if problems[0] != "Key 'nonexistent' not found in credential 'github_oauth'" {
		t.Fatalf("problem[0] = %q", problems[0])
	}
	if problems[1] != "Credential 'missing' not found" {
		t.Fatalf("problem[1] = %q", problems[1])
	}
	if problems[2] != "Credential 'empty' has no keys" {
		t.Fatalf("problem[2] = %q", problems[2])
	}

	required := resolver.GetRequiredCredentials("{{a.x}} {{b.y}} {{a.z}}")
	if len(required) != 2 || required[0] != "a" || required[1] != "b" {
		t.Fatalf("required = %v", required)
	}
}
