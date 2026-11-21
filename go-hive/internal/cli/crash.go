package cli

// Parity: framework/cli.py::main, framework/credentials/cli.py::main
//
// Top-level crash presentation. CPython turns an uncaught exception into a
// traceback on stderr and exit status 1; a Go panic in a CLI command is the
// direct analogue and must not classify differently, so GuardMain keeps the
// panic value + real goroutine frames on stderr but maps the runtime's exit
// status 2 to 1 (tracker.ExitDomain), matching the harness's treatment of
// returned unclassified errors (internal/tracker/errors.go::writeCrash).

import (
	"fmt"
	"os"
	"reflect"
	"runtime/debug"
	"strings"

	"github.com/aden-hive/hive-go/internal/tracker"
)

// GuardMain runs one CLI entry point and converts a panic into the Python
// crash presentation. Usage: os.Exit(cli.GuardMain(func() int { ... })).
func GuardMain(run func() int) (code int) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		fmt.Fprintln(os.Stderr, "Traceback (most recent call last):")
		stack := strings.TrimRight(string(debug.Stack()), "\n")
		fmt.Fprintln(os.Stderr, stack)
		fmt.Fprintf(os.Stderr, "%s: %v\n", panicTypeName(recovered), recovered)
		code = tracker.ExitDomain
	}()
	return run()
}

func panicTypeName(value any) string {
	if err, ok := value.(error); ok {
		value = err
	}
	typ := reflect.TypeOf(value)
	if typ == nil {
		return "Exception"
	}
	if typ.Kind() == reflect.Pointer {
		return typ.Elem().Name()
	}
	return typ.Name()
}
