package cli

// Parity: framework/credentials/cli.py

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/tracker"
)

type credAdapter interface {
	Store() *credentials.CredentialStore
	GetAllAccountInfo() []map[string]any
}

var credAdapterDefault = func() (credAdapter, error) {
	return credentials.DefaultAdapter(credentials.CatalogNamedSpecs())
}

type CredRunner struct {
	Stdout io.Writer
	Stderr io.Writer
}

func NewCredRunner() *CredRunner {
	return &CredRunner{Stdout: os.Stdout, Stderr: os.Stderr}
}

func setupCredLogging() {
	path := os.Getenv("HIVE_CRED_LOG")
	if path == "" {
		// Upstream installs a NullHandler when HIVE_CRED_LOG is unset, so the
		// command's stderr stays clean; the Go default logger would leak
		// package log lines (e.g. encryption-key notices) onto stderr.
		credentials.SetLogSink(nil)
		log.SetOutput(io.Discard)
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		credentials.SetLogSink(nil)
		log.SetOutput(io.Discard)
		return
	}
	// The credential packages write through the Python-format sink; the
	// standard logger keeps whatever else lands there (dated lines) in the
	// same file.
	credentials.SetLogSink(file)
	log.SetOutput(file)
	log.SetFlags(0)
}

func (r *CredRunner) Run(argv []string) int {
	setupCredLogging()
	asJSON := false
	for _, token := range argv {
		if token == "--json" {
			asJSON = true
		}
	}

	spec := credCommand()
	args, err := parseCommand(spec, argv)
	if err != nil {
		var help *helpRequest
		if errors.As(err, &help) {
			fmt.Fprint(r.Stdout, help.spec.helpText())
			return tracker.ExitOK
		}
		var usage *usageError
		if errors.As(err, &usage) {
			return r.reportUsageError(spec, usage, asJSON)
		}
		fmt.Fprintf(r.Stderr, "%s\n", err.Error())
		return 2
	}

	return tracker.Run(func() (any, error) {
		return r.execute(args)
	}, asJSON, r.render, r.Stdout, r.Stderr)
}

func (r *CredRunner) reportUsageError(root *commandSpec, usage *usageError, asJSON bool) int {
	prog := usage.prog
	if prog == "" {
		prog = "hive-cred"
	}
	cliErr := tracker.Validation(prog + ": " + usage.msg)
	if asJSON {
		tracker.WriteIndentedJSON(r.Stdout, cliErr.Envelope())
		return cliErr.ExitCode
	}
	// Upstream's _Parser.error prints the responsible page's usage block, then
	// "error: <prog>: <message>" — the leaf's page for a leaf-level failure,
	// the root's for argparse's deferred "unrecognized arguments".
	if block := usageErrorBlock(root, usage); block != "" {
		fmt.Fprintln(r.Stderr, block)
	}
	fmt.Fprintf(r.Stderr, "error: %s\n", cliErr.Message)
	return cliErr.ExitCode
}

func findSpecByProg(spec *commandSpec, prog string) *commandSpec {
	if spec.Prog == prog {
		return spec
	}
	for _, sub := range spec.Subcommands {
		if found := findSpecByProg(sub, prog); found != nil {
			return found
		}
	}
	return nil
}

func (r *CredRunner) execute(args *parsedArgs) (any, error) {
	switch args.cmd.Dest {
	case "token":
		return cmdCredToken(args)
	case "browse":
		return cmdCredBrowse(args)
	case "inspect":
		return cmdCredInspect(args)
	}
	return nil, nil
}

func unavailableError(err error) *tracker.CLIError {
	return &tracker.CLIError{
		Code:     "unavailable",
		Message:  "credential store unavailable: " + err.Error(),
		ExitCode: tracker.ExitPermission,
	}
}

func cmdCredToken(args *parsedArgs) (any, error) {
	credentialID := strings.TrimSpace(args.str("credential_id"))
	if credentialID == "" {
		return nil, tracker.Validation("credential_id is required.")
	}
	account := strings.TrimSpace(args.str("account"))
	key := strings.TrimSpace(args.str("key"))

	adapter, err := credAdapterDefault()
	if err != nil {
		return nil, unavailableError(err)
	}
	if adapter == nil {
		return nil, unavailableError(errors.New("no adapter"))
	}

	resolved, err := credentials.ResolveSecret(adapter, credentialID, account, key)
	if err != nil {
		var notFound *credentials.SecretNotFound
		if errors.As(err, &notFound) {
			return nil, tracker.NotFound(
				"no value stored for "+notFound.Error()+". `credential_id` is the credential name (e.g. `google`), not the opaque "+
					"account id reported by get_account_info."+knownCredentialIDsHint()+" "+
					"Run `hive-cred browse` for the accounts and their aliases, then retry with the right --account.",
				tracker.Detail{Key: "credential_id", Value: credentialID},
				tracker.Detail{Key: "account", Value: nullableString(account)},
			)
		}
		var expired *credentials.CredentialExpiredError
		if errors.As(err, &expired) {
			message := credentialID
			if account != "" {
				message += "/" + account
			}
			message += " is expired and could not be refreshed. The user must reconnect it; do not retry."
			return nil, &tracker.CLIError{
				Code:     "reauth_required",
				Message:  message,
				ExitCode: tracker.ExitDomain,
				Details: []tracker.Detail{
					{Key: "credential_id", Value: credentialID},
					{Key: "account", Value: nullableString(account)},
					{Key: "help_url", Value: nullableString(expired.HelpURL)},
				},
			}
		}
		return nil, err
	}

	result := credTokenResult{
		OK:               true,
		CredentialID:     resolved.CredentialID,
		Account:          resolved.Account,
		Key:              resolved.Key,
		Value:            resolved.Value,
		ExpiresInSeconds: resolved.ExpiresInSeconds(),
	}
	if resolved.ExpiresAt != nil {
		formatted := pythonISOFormat(*resolved.ExpiresAt)
		result.ExpiresAt = &formatted
	}
	return result, nil
}

func cmdCredBrowse(args *parsedArgs) (any, error) {
	adapter, err := credAdapterDefault()
	if err != nil {
		return nil, unavailableError(err)
	}
	if adapter == nil {
		return nil, unavailableError(errors.New("no adapter"))
	}
	return credentials.Browse(adapter, args.str("query"), credentials.CatalogNamedSpecs()), nil
}

func cmdCredInspect(args *parsedArgs) (any, error) {
	credentialID := strings.TrimSpace(args.str("credential_id"))
	if credentialID == "" {
		return nil, tracker.Validation("credential_id is required.")
	}
	adapter, err := credAdapterDefault()
	if err != nil {
		return nil, unavailableError(err)
	}
	if adapter == nil {
		return nil, unavailableError(errors.New("no adapter"))
	}
	return credentials.Inspect(adapter, credentialID, strings.TrimSpace(args.str("account"))), nil
}

func knownCredentialIDsHint() string {
	adapter, err := credAdapterDefault()
	if err != nil || adapter == nil {
		return ""
	}
	ids := make([]string, 0)
	seen := map[string]struct{}{}
	for _, acct := range adapter.GetAllAccountInfo() {
		value, ok := acct["credential_id"]
		if !ok || !credTruthy(value) {
			continue
		}
		text := fmt.Sprint(value)
		if _, duplicate := seen[text]; duplicate {
			continue
		}
		seen[text] = struct{}{}
		ids = append(ids, text)
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return " Known credential ids: " + strings.Join(ids, ", ") + "."
}

func credTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return true
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

type credTokenResult struct {
	OK               bool    `json:"ok"`
	CredentialID     string  `json:"credential_id"`
	Account          *string `json:"account"`
	Key              string  `json:"key"`
	Value            string  `json:"value"`
	ExpiresAt        *string `json:"expires_at"`
	ExpiresInSeconds *int    `json:"expires_in_seconds"`
}

func (r *CredRunner) render(result any) {
	switch typed := result.(type) {
	case credTokenResult:
		fmt.Fprintln(r.Stdout, typed.Value)
	case credentials.BrowseResult:
		fmt.Fprintln(r.Stdout, credentials.RenderBrowse(typed))
	case credentials.InspectResult:
		fmt.Fprintln(r.Stdout, credentials.RenderInspect(typed))
	default:
		fmt.Fprintf(r.Stdout, "%v\n", result)
	}
}

func pythonISOFormat(value time.Time) string {
	value = value.UTC()
	out := value.Format("2006-01-02T15:04:05")
	if micro := value.Nanosecond() / 1000; micro != 0 {
		out += fmt.Sprintf(".%06d", micro)
	}
	return out + "+00:00"
}

// Parity: framework/credentials/cli.py::_build_parser
func credCommand() *commandSpec {
	token := &commandSpec{
		Prog:           "hive-cred token",
		Help:           "print a fresh value (OAuth tokens are refreshed first)",
		Dest:           "token",
		RawDescription: true,
		Positionals:    []positionalSpec{{Dest: "credential_id", Required: true, Help: "provider / credential id, e.g. google, apollo"}},
		Flags: []argSpec{
			{Dest: "json", Long: "json", Kind: kindBool, Help: "emit a JSON envelope on stdout"},
			{Dest: "account", Long: "account", Kind: kindString, Default: "", Metavar: "ALIAS", Help: "connected-account alias, e.g. email3 (required when several are connected)"},
			{Dest: "key", Long: "key", Kind: kindString, Default: "", Metavar: "NAME", Help: "which key to print (default: the credential's primary key, e.g. access_token)"},
		},
		Epilog: "Call this right before each request rather than once per script: the value printed is\n" +
			"fresh now, and OAuth access tokens last about an hour.\n" +
			"\n" +
			"examples:\n" +
			"  TOK=$(hive-cred token google --account email3)\n" +
			"  hive-cred token google --account email3 --json     # adds expires_at / expires_in_seconds\n" +
			"  hive-cred token apollo --key api_key",
	}
	browse := &commandSpec{
		Prog:           "hive-cred browse",
		Help:           "list connected accounts and connectable providers (metadata only)",
		Dest:           "browse",
		RawDescription: true,
		Positionals:    []positionalSpec{{Dest: "query", Required: false, Help: "optional filter on provider / alias / identity"}},
		Flags: []argSpec{
			{Dest: "json", Long: "json", Kind: kindBool, Help: "emit a JSON envelope on stdout"},
		},
		Epilog: "examples:\n  hive-cred browse --json\n  hive-cred browse google",
	}
	inspect := &commandSpec{
		Prog:           "hive-cred inspect",
		Help:           "show one credential's accounts, key names and type (no values)",
		Dest:           "inspect",
		RawDescription: true,
		Positionals:    []positionalSpec{{Dest: "credential_id", Required: true, Help: "provider / credential id, e.g. google"}},
		Flags: []argSpec{
			{Dest: "json", Long: "json", Kind: kindBool, Help: "emit a JSON envelope on stdout"},
			{Dest: "account", Long: "account", Kind: kindString, Default: "", Metavar: "ALIAS", Help: "restrict to one connected-account alias"},
		},
		Epilog: "examples:\n  hive-cred inspect google --json\n  hive-cred inspect google --account email3",
	}
	return &commandSpec{
		Prog:        "hive-cred",
		Description: "Fetch a fresh credential value for use in a script.",
		Dest:        "command",
		Subcommands: []*commandSpec{token, browse, inspect},
	}
}
