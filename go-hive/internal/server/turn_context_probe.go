package server

// The built-in probe turn-context package.
//
// Upstream installs turn-context packages into its Python environment and
// discovers them through the `hive.turn_context` entry-point group. A Go
// binary has no entry points, so an installed package here is a factory
// linked into the binary. This one ships in every build under the name
// `probe` and starts when configuration.json enables it:
//
//	{"turn_context": {"probe": {"enabled": true}}}
//
// It contributes one fixed sentence ahead of each user turn and is otherwise
// inert: disabled or unconfigured it injects nothing (the startup log still
// reports it installed but off, like any installed package). That makes the
// installation surface observable end to end — the configuration gate, the
// runtime, the reminder hub's wrapper and the user-turn position all run for
// real. The prompt-parity probe installs the same package name on the Python
// reference side (go_hive/scripts/prompt_system_parity.py), so both runtimes
// answer the same configuration and their injected text can be compared byte
// for byte.
//
// Parity: framework/server/turn_context.py (entry-point discovery; the Python
// counterpart is the fake probe dist-info the parity script installs).

// TurnContextProbeName is the package name the built-in probe registers under.
const TurnContextProbeName = "probe"

// TurnContextProbeBody is the text the probe contributes. The Python probe in
// the parity script returns the same sentence (45 chars).
const TurnContextProbeBody = "Turn-context probe body for the parity probe."

type turnContextProbe struct{}

func (turnContextProbe) Start() error { return nil }

func (turnContextProbe) Stop() error { return nil }

func (turnContextProbe) Context(string, string) (string, error) {
	return TurnContextProbeBody, nil
}

func init() {
	RegisterTurnContextProvider(TurnContextProbeName, func(TurnContextHost) (TurnContext, error) {
		return turnContextProbe{}, nil
	})
}
