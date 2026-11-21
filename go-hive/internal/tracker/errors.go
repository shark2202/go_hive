package tracker

// Parity: framework/trackerdb/errors.py

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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
			// Unclassified failure. Upstream's harness (framework/trackerdb/
			// errors.py::run) catches only TrackerError, so anything else
			// escapes as an uncaught exception: a traceback on stderr, exit 1,
			// and no JSON envelope even with --json. Go must not launder a bug
			// into a domain_error envelope; render the same crash shape with
			// the real Go frames.
			writeCrash(stderr, err)
			return ExitDomain
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

// writeCrash renders an unclassified error as a Python-style traceback: header,
// one `File "…", line N, in func` frame per Go frame (outermost first, like
// CPython), then `<type>: <message>`. Exit code stays 1 (EXIT_DOMAIN), matching
// Python's uncaught-exception exit.
func writeCrash(stderr io.Writer, err error) {
	fmt.Fprintln(stderr, "Traceback (most recent call last):")
	frames := callerFrames()
	for index := len(frames) - 1; index >= 0; index-- {
		frame := frames[index]
		fmt.Fprintf(stderr, "  File %q, line %d, in %s\n", crashFile(frame.File), frame.Line, frame.Function)
	}
	fmt.Fprintf(stderr, "%s: %v\n", errorTypeName(err), err)
}

func callerFrames() []runtime.Frame {
	pcs := make([]uintptr, 32)
	// Skip runtime.Callers, callerFrames and writeCrash themselves.
	count := runtime.Callers(3, pcs)
	if count == 0 {
		return nil
	}
	frames := runtime.CallersFrames(pcs[:count])
	collected := make([]runtime.Frame, 0, count)
	for {
		frame, more := frames.Next()
		if !strings.HasPrefix(frame.Function, "runtime.") {
			collected = append(collected, frame)
		}
		if !more {
			break
		}
	}
	return collected
}

// crashFile shortens a source path to the last two elements so the traceback
// stays readable (Python prints the path it was invoked with; the Go binary
// only knows the build path, which would leak the builder's checkout).
func crashFile(path string) string {
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) <= 2 {
		return path
	}
	return filepath.Join(parts[len(parts)-2:]...)
}

func errorTypeName(err error) string {
	value := reflect.TypeOf(err)
	if value == nil {
		return "Exception"
	}
	if value.Kind() == reflect.Pointer {
		return value.Elem().Name()
	}
	return value.Name()
}

func WriteIndentedJSON(w io.Writer, value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		encoded, _ = json.Marshal(fmt.Sprint(value))
	}
	w.Write(encoded)
	io.WriteString(w, "\n")
}
