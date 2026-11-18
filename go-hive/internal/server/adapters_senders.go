package server

// Integration seam for the email-senders tool suite (internal/senders).
//
// The engine, the nine actions, the local sender store, rotation, the
// do-not-contact list and the send log all live in package senders. This file
// is the single adapter the wiring lane needs: it enforces the upstream
// registration gate and installs the tool into whichever *tools.ToolRegistry
// the session tool catalogue builds.
//
// Call it from the session tool catalogue assembly (internal/cli), next to the
// builtin toolbox and integrations.New(...).Install(...). It is not an HTTP
// route: the gate has to sit at registration so a disabled suite is invisible
// to the model, not merely hidden from an allowlist.

import (
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/senders"
	"github.com/aden-hive/hive-go/internal/tools"
)

// RegisterSenderTools installs the "senders" tool when the email_senders
// feature is enabled. It returns the number of tools installed (0 when the
// feature is off or the registry is nil) and an error only when the local
// sender store cannot be opened.
func RegisterSenderTools(registry *tools.ToolRegistry, home hivehome.Home) (int, error) {
	if registry == nil {
		return 0, nil
	}
	if !EmailSendersEnabled(home) {
		return 0, nil
	}
	return senders.Install(registry, home.Root)
}
