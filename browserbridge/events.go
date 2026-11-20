package browserbridge

// EventSink receives unsolicited extension events. L0/L1 only publishes them;
// L2/L3 consumers (context registry, stale sweep, health blockers, telemetry)
// plug in here instead of the socket reader growing feature branches.
type EventSink interface {
	ConnectionEvent(event, profile, reason string)
	CDPEvent(profile string, tabID int, method string, params map[string]any)
	TabEvent(profile, event string, tabID, groupID, openerTabID int, active *bool)
	TabGroupEvent(profile, event string, groupID int)
}

type nopEvents struct{}

func (nopEvents) ConnectionEvent(string, string, string)        {}
func (nopEvents) CDPEvent(string, int, string, map[string]any)  {}
func (nopEvents) TabEvent(string, string, int, int, int, *bool) {}
func (nopEvents) TabGroupEvent(string, string, int)             {}
