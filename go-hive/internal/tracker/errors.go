package tracker

// Parity: framework/trackerdb/errors.py

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ExitOK            = 0
	ExitDomain        = 1
	ExitNoColony      = 2
	ExitPermission    = 3
	ExitNotFound      = 4
	ExitNotRegistered = 5
	ExitValidation    = 6
	ExitDenied        = 7
	ExitPartial       = 9
	ExitBusy          = 10
)

type Detail struct {
	Key   string
	Value any
}

type CLIError struct {
	Code     string
	Message  string
	ExitCode int
	Details  []Detail
}

func (e *CLIError) Error() string { return e.Message }

type errorBody struct {
	code    string
	message string
	details []Detail
}

func (b errorBody) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	write := func(key string, value any, first bool) error {
		if !first {
			buf.WriteByte(',')
		}
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return err
		}
		buf.Write(keyJSON)
		buf.WriteString(": ")
		valueJSON, err := marshalDefault(value)
		if err != nil {
			return err
		}
		buf.Write(valueJSON)
		return nil
	}
	if err := write("code", b.code, true); err != nil {
		return nil, err
	}
	if err := write("message", b.message, false); err != nil {
		return nil, err
	}
	for _, detail := range b.details {
		if err := write(detail.Key, detail.Value, false); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

type envelope struct {
	body errorBody
}

func (e envelope) MarshalJSON() ([]byte, error) {
	bodyJSON, err := json.Marshal(e.body)
	if err != nil {
		return nil, err
	}
	return append([]byte(`{"ok": false, "error": `), append(bodyJSON, '}')...), nil
}

func (e *CLIError) Envelope() any {
	return envelope{body: errorBody{code: e.Code, message: e.Message, details: e.Details}}
}

func marshalDefault(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err == nil {
		return encoded, nil
	}
	return json.Marshal(fmt.Sprint(value))
}

func Validation(message string, details ...Detail) *CLIError {
	return &CLIError{Code: "validation", Message: message, ExitCode: ExitValidation, Details: details}
}

func NotFound(message string, details ...Detail) *CLIError {
	return &CLIError{Code: "not_found", Message: message, ExitCode: ExitNotFound, Details: details}
}

func NotRegistered(table string, details ...Detail) *CLIError {
	all := append([]Detail{{Key: "table", Value: table}}, details...)
	return &CLIError{
		Code: "not_registered",
		Message: fmt.Sprintf(
			"table %s is not registered for worker writes. The queen must run "+
				"`hive-tracker register %s --write-columns ... --key-columns ...` first.",
			pythonRepr(table), table),
		ExitCode: ExitNotRegistered,
		Details:  all,
	}
}

func Permission(message string, details ...Detail) *CLIError {
	return &CLIError{Code: "permission_denied", Message: message, ExitCode: ExitPermission, Details: details}
}

func Denied(message string, details ...Detail) *CLIError {
	return &CLIError{Code: "denied", Message: message, ExitCode: ExitDenied, Details: details}
}

func Domain(message string, details ...Detail) *CLIError {
	return &CLIError{Code: "domain_error", Message: message, ExitCode: ExitDomain, Details: details}
}

func Busy(message string, details ...Detail) *CLIError {
	return &CLIError{Code: "busy", Message: message, ExitCode: ExitBusy, Details: details}
}

func FromSQLite(err error, what string) *CLIError {
	text := ""
	if err != nil {
		text = err.Error()
	}
	lower := bytes.ToLower([]byte(text))
	if bytes.Contains(lower, []byte("locked")) || bytes.Contains(lower, []byte("busy")) {
		return Busy(fmt.Sprintf(
			"%s: %s. Another worker or the queen is writing to this tracker right now — wait a moment and re-run the same command.",
			what, text))
	}
	return Domain(fmt.Sprintf("%s: %s", what, text))
}

func NoColony() *CLIError {
	return &CLIError{
		Code: "no_colony",
		Message: "no colony context — the tracker only exists inside a colony. The queen " +
			"reaches colony mode by calling suggest_colony and waiting for the user to " +
			"confirm the Create Colony popup; workers inherit it automatically. " +
			"(Debug: pass --colony <name>.)",
		ExitCode: ExitNoColony,
	}
}

func pythonRepr(text string) string {
	return "'" + text + "'"
}

func Run(fn func() (any, error), asJSON bool, render func(any), stdout, stderr io.Writer) int {
	result, err := fn()
	if err != nil {
		var cliErr *CLIError
		if !errors.As(err, &cliErr) {
			cliErr = Domain(err.Error())
		}
		if asJSON {
			WriteIndentedJSON(stdout, cliErr.Envelope())
		} else {
			fmt.Fprintf(stderr, "error: %s\n", cliErr.Message)
		}
		return cliErr.ExitCode
	}
	if asJSON {
		WriteIndentedJSON(stdout, result)
		return ExitOK
	}
	if render != nil {
		render(result)
	}
	return ExitOK
}

func WriteIndentedJSON(w io.Writer, value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		encoded, _ = json.Marshal(fmt.Sprint(value))
	}
	w.Write(encoded)
	io.WriteString(w, "\n")
}
