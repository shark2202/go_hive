// Google Analytics 4 and Google Search Console (upstream google_analytics_tool
// and google_search_console_tool, 7 + 8 actions).
//
// GA4 speaks the Data API v1beta REST surface the Python client wraps:
//
//	POST /{property}:runReport
//	POST /{property}:runRealtimeReport
//
// Search Console uses two roots: webmasters/v3 for analytics and sitemaps,
// searchconsole/v1 for URL inspection.
//
// Authentication accepts an already-minted OAuth token (WithCredential, or the
// module's environment variables — GOOGLE_ACCESS_TOKEN is shared) and
// otherwise mints one from a service-account JSON at
// GOOGLE_APPLICATION_CREDENTIALS with the read-only scope for that API. gcloud
// application-default credential files are not read; export a token instead.
package integrations

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ------------------------------------------------------- shared credentials ---

// googleAuthError renders the GA "not configured" map.
func googleAuthError(detail string) map[string]any {
	message := "Google Analytics credentials not configured"
	if detail != "" {
		message += ": " + detail
	}
	return map[string]any{
		"error": message,
		"help": "Set GOOGLE_APPLICATION_CREDENTIALS to your service account JSON path, " +
			"or set GA_ACCESS_TOKEN / GOOGLE_ACCESS_TOKEN to an OAuth access token",
	}
}

// googleToken resolves a bearer token for one Google API: the direct
// token first, then a service-account JWT exchange with the given scope.
func googleToken(ctx context.Context, rt *Runtime, moduleID, scope string) (string, map[string]any) {
	if token := moduleToken(rt, moduleID); token != "" {
		return token, nil
	}
	credentialsPath := strings.TrimSpace(runtimeEnv(rt, "GOOGLE_APPLICATION_CREDENTIALS"))
	if credentialsPath == "" {
		return "", nil
	}
	token, _, failure := googleServiceAccountToken(ctx, rt, credentialsPath, scope)
	if failure != nil {
		return "", failure
	}
	return token, nil
}

// googleServiceAccountToken exchanges a service-account JSON key
// for an OAuth access token using the JWT bearer grant.
func googleServiceAccountToken(ctx context.Context, rt *Runtime, path, scope string) (string, string, map[string]any) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: " + err.Error()}
	}
	var key struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
		ProjectID   string `json:"project_id"`
	}
	if err := json.Unmarshal(raw, &key); err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: invalid service account JSON: " + err.Error()}
	}
	if key.ClientEmail == "" || key.PrivateKey == "" {
		return "", "", map[string]any{
			"error": "Google authentication failed: service account JSON is missing client_email or private_key",
		}
	}
	tokenURI := key.TokenURI
	if tokenURI == "" {
		tokenURI = marketingGoogleTokenURI
	}
	assertion, err := googleJWT(key.ClientEmail, key.PrivateKey, tokenURI, scope, runtimeNow(rt).UTC())
	if err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: " + err.Error()}
	}
	form := url.Values{
		"grant_type": []string{"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  []string{assertion},
	}
	resp, err := marketingDoForm(ctx, rt, tokenURI, nil, form, 30*time.Second)
	if err != nil {
		return "", "", map[string]any{"error": "Google authentication failed: " + err.Error()}
	}
	if resp.Status >= 400 {
		return "", "", map[string]any{
			"error": "Google authentication failed: token endpoint returned HTTP " + strconv.Itoa(resp.Status),
		}
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return "", "", map[string]any{"error": "Google authentication failed: token endpoint returned invalid JSON"}
	}
	accessToken := stringOr(data, "access_token", "")
	if accessToken == "" {
		return "", "", map[string]any{"error": "Google authentication failed: token endpoint returned no access_token"}
	}
	return accessToken, key.ProjectID, nil
}

// googleJWT builds an RS256-signed JWT for the service-account
// OAuth flow with an explicit scope.
func googleJWT(email, privateKeyPEM, audience, scope string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", fmt.Errorf("service account private_key is not PEM encoded")
	}
	var key *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("service account private_key is not RSA")
		}
		key = rsaKey
	} else if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = parsed
	} else {
		return "", fmt.Errorf("service account private_key could not be parsed")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := map[string]any{
		"iss":   email,
		"scope": scope,
		"aud":   audience,
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := header + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
