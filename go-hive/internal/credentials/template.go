package credentials

import (
	"fmt"
	"regexp"
	"strings"
)

var templatePattern = regexp.MustCompile(`\{\{([a-zA-Z0-9_-]+)(?:\.([a-zA-Z0-9_-]+))?\}\}`)

type TemplateReference struct {
	CredentialID string
	Key          *string
}

type TemplateResolver struct {
	store *CredentialStore
}

func NewTemplateResolver(store *CredentialStore) *TemplateResolver {
	return &TemplateResolver{store: store}
}

func (r *TemplateResolver) Resolve(template string) (string, error) {
	return r.ResolveWithMissing(template, true)
}

func (r *TemplateResolver) ResolveWithMissing(template string, failOnMissing bool) (string, error) {
	matches := templatePattern.FindAllStringSubmatchIndex(template, -1)
	if len(matches) == 0 {
		return template, nil
	}

	var out strings.Builder
	last := 0
	for _, m := range matches {
		out.WriteString(template[last:m[0]])
		credID := template[m[2]:m[3]]
		var keyName string
		hasKey := m[4] >= 0
		if hasKey {
			keyName = template[m[4]:m[5]]
		}

		credential, err := r.store.GetCredential(credID, true)
		if err != nil {
			return "", err
		}
		if credential == nil {
			if failOnMissing {
				return "", newCredentialNotFoundError("Credential '%s' not found", credID)
			}
			out.WriteString(template[m[0]:m[1]])
			last = m[1]
			continue
		}

		var value *string
		if hasKey {
			value = credential.GetKey(keyName)
			if value == nil {
				return "", newCredentialKeyNotFoundError("Key '%s' not found in credential '%s'", keyName, credID)
			}
		} else {
			value = credential.GetDefaultKey()
			if value == nil {
				return "", newCredentialKeyNotFoundError("Credential '%s' has no keys", credID)
			}
		}

		credential.RecordUsage()
		out.WriteString(*value)
		last = m[1]
	}
	out.WriteString(template[last:])
	return out.String(), nil
}

func (r *TemplateResolver) ResolveHeaders(headerTemplates map[string]string) (map[string]string, error) {
	return r.ResolveHeadersWithMissing(headerTemplates, true)
}

func (r *TemplateResolver) ResolveHeadersWithMissing(headerTemplates map[string]string, failOnMissing bool) (map[string]string, error) {
	out := make(map[string]string, len(headerTemplates))
	for key, value := range headerTemplates {
		resolved, err := r.ResolveWithMissing(value, failOnMissing)
		if err != nil {
			return nil, err
		}
		out[key] = resolved
	}
	return out, nil
}

func (r *TemplateResolver) ResolveParams(paramTemplates map[string]string) (map[string]string, error) {
	return r.ResolveParamsWithMissing(paramTemplates, true)
}

func (r *TemplateResolver) ResolveParamsWithMissing(paramTemplates map[string]string, failOnMissing bool) (map[string]string, error) {
	out := make(map[string]string, len(paramTemplates))
	for key, value := range paramTemplates {
		resolved, err := r.ResolveWithMissing(value, failOnMissing)
		if err != nil {
			return nil, err
		}
		out[key] = resolved
	}
	return out, nil
}

func (r *TemplateResolver) HasTemplates(text string) bool {
	return templatePattern.MatchString(text)
}

func (r *TemplateResolver) ExtractReferences(text string) []TemplateReference {
	matches := templatePattern.FindAllStringSubmatch(text, -1)
	out := make([]TemplateReference, 0, len(matches))
	for _, m := range matches {
		ref := TemplateReference{CredentialID: m[1]}
		if m[2] != "" {
			key := m[2]
			ref.Key = &key
		}
		out = append(out, ref)
	}
	return out
}

func (r *TemplateResolver) ValidateReferences(text string) ([]string, error) {
	errors := []string{}
	for _, ref := range r.ExtractReferences(text) {
		credential, err := r.store.GetCredential(ref.CredentialID, false)
		if err != nil {
			return nil, err
		}
		if credential == nil {
			errors = append(errors, fmt.Sprintf("Credential '%s' not found", ref.CredentialID))
			continue
		}
		if ref.Key != nil {
			if !credential.HasKey(*ref.Key) {
				errors = append(errors, fmt.Sprintf("Key '%s' not found in credential '%s'", *ref.Key, ref.CredentialID))
			}
		} else if credential.Keys.Len() == 0 {
			errors = append(errors, fmt.Sprintf("Credential '%s' has no keys", ref.CredentialID))
		}
	}
	return errors, nil
}

func (r *TemplateResolver) GetRequiredCredentials(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, ref := range r.ExtractReferences(text) {
		if !seen[ref.CredentialID] {
			seen[ref.CredentialID] = true
			out = append(out, ref.CredentialID)
		}
	}
	return out
}
