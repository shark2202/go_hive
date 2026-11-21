// Freshdesk contact actions (split from freshdesk.go).

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

func batch6Lane2FreshdeskListContacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(clampInt(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(clampInt(intArg(input, "per_page", 30), 1, 100))},
	}
	if email := stringArg(input, "email"); email != "" {
		query.Set("email", email)
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/contacts", query, nil)
	if !ok {
		return value, nil
	}
	contacts := batch6Lane2FreshdeskItems(value, batch6Lane2FreshdeskContact)
	return map[string]any{"contacts": contacts, "count": len(contacts)}, nil
}

func batch6Lane2FreshdeskGetContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	contactID, hasID := optionalInt(input, "contact_id")
	email := stringArg(input, "email")
	if (!hasID || contactID == 0) && email == "" {
		return map[string]any{"error": "contact_id or email is required"}, nil
	}
	if hasID && contactID != 0 {
		value, ok := batch6Lane2FreshdeskCall(
			ctx, rt, http.MethodGet, fmt.Sprintf("/contacts/%d", contactID), nil, nil,
		)
		if !ok {
			return value, nil
		}
		return batch6Lane2FreshdeskContact(asMap(value)), nil
	}
	query := url.Values{"email": {email}}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/contacts", query, nil)
	if !ok {
		return value, nil
	}
	if items := asSlice(value); len(items) == 0 {
		if object := asMap(value); len(object) == 0 {
			return map[string]any{"error": "Contact not found"}, nil
		}
		return batch6Lane2FreshdeskContact(asMap(value)), nil
	}
	return batch6Lane2FreshdeskContact(asMap(asSlice(value)[0])), nil
}

func batch6Lane2FreshdeskCreateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	email := stringArg(input, "email")
	if email == "" {
		return map[string]any{"error": "email is required"}, nil
	}
	payload := map[string]any{"email": email}
	if name := stringArg(input, "name"); name != "" {
		payload["name"] = name
	}
	if phone := stringArg(input, "phone"); phone != "" {
		payload["phone"] = phone
	}
	if companyID, present := optionalInt(input, "company_id"); present {
		payload["company_id"] = companyID
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodPost, "/contacts", nil, payload)
	if !ok {
		return value, nil
	}
	return batch6Lane2FreshdeskContact(asMap(value)), nil
}

func batch6Lane2FreshdeskUpdateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	contactID := intArg(input, "contact_id", 0)
	if contactID == 0 {
		return map[string]any{"error": "contact_id is required"}, nil
	}
	payload := map[string]any{}
	if name, present := optionalString(input, "name"); present {
		payload["name"] = name
	}
	if email, present := optionalString(input, "email"); present {
		payload["email"] = email
	}
	if phone, present := optionalString(input, "phone"); present {
		payload["phone"] = phone
	}
	if companyID, present := optionalInt(input, "company_id"); present {
		payload["company_id"] = companyID
	}
	if len(payload) == 0 {
		return map[string]any{
			"error": "At least one field (name, email, phone, company_id) is required",
		}, nil
	}
	value, ok := batch6Lane2FreshdeskCall(
		ctx, rt, http.MethodPut, fmt.Sprintf("/contacts/%d", contactID), nil, payload,
	)
	if !ok {
		return value, nil
	}
	return batch6Lane2FreshdeskContact(asMap(value)), nil
}
