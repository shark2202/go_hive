// Package channels hosts Hive channel plugins as local subprocesses over a
// line-delimited JSON protocol.
//
// Upstream Hive (framework/server/channels.py) loads channel packages through
// the “hive.channels“ Python entry-point group and calls their start/stop
// in-process; a channel talks to the queen through the ChannelHost object
// (open_session, send, subscribe, unsubscribe, set_title, judge, state_dir,
// config) and receives QueenOutput values (message, question, error, files,
// idle). Go has no entry points, so this package keeps the same object model
// across a process boundary:
//
//	configuration.json
//	  channels.<name>.enabled   start this plugin when truthy
//	  channels.<name>.command   argv (array of strings) or the executable (string)
//	  channels.<name>.args      extra argv entries
//	  channels.<name>.cwd       working directory (default: the serve process cwd)
//	  channels.<name>.env       KEY=value entries appended to the inherited env
//	  every other key           passed unchanged in the hello frame's config
//
// An enabled section with no command is reported failed, never skipped:
// "no command configured (Go host uses stdio plugins; Python hive.channels
// entry points need the Python adapter)".
//
// # Wire protocol
//
// One UTF-8 JSON object per line on the child's stdin/stdout, JSON-RPC 2.0
// shaped: requests carry id/method/params and are answered by id; responses
// carry result or error; notifications carry method/params and no id. stdout
// is protocol-only; the child's logs go to stderr and are forwarded to the
// host logger. stdlib “json“ on the plugin side is enough. The protocol
// version is "hive-channel/1".
//
// Host → plugin (requests, in order):
//
//	hello  params {protocol, name, config, state_dir}
//	       result {plugin_version, capabilities?}
//	start  params {}                       (30s budget)
//	stop   params {}                       (30s budget; the child is killed
//	                                       if it does not answer in time)
//
// Plugin → host (requests):
//
//	open_session params {queen_id, session_id?, title?} result {session_id}
//	send         params {session_id, text, images?: [base64], display_text?,
//	             correlation_id?} result {session_id}
//	subscribe    params {session_id} result {subscription_id}
//	unsubscribe  params {subscription_id} result {}
//	set_title    params {session_id, title} result {}
//	judge        params {question, state, criteria?} result {probability: float|null}
//
// Images are base64 (a data: URI is accepted too); at most 8 per send,
// 4 MiB decoded per image and 8 MiB decoded across the send. Frames are
// capped at 16 MiB. The host never splits
// or pretty-prints a frame.
//
// # Queen outputs
//
// After subscribe, the host sends notification:
//
//	output params {subscription_id, session_id, kind, text, questions?,
//	               files?, seq}
//
// kind is message, question, error, files or idle. questions is the ask_user
// list ({"id","prompt","options"?}). files are absolute paths to the copies in
// the session's data/attachments: the plugin runs on the same machine and
// reads them directly (upstream's QueenOutput.files are Paths). A normalized
// file copy should upload the bytes itself; base64 file transport is not part
// of the contract.
//
// Each subscription has one delivery goroutine, so outputs arrive in order.
// seq starts at 1 and increments per subscription; a gap means the host
// dropped frames under backpressure. The queue is bounded (256 by default)
// and drops the OLDEST output when full, so the plugin always trends toward
// the latest state and the queen's event bus is never blocked. Plugins that
// must not miss an output should drain promptly or unsubscribe.
//
// # Errors
//
// A failed plugin request is a JSON-RPC error. data.kind names the condition:
//
//	no_queen          no such queen id (check channels.<name> settings)
//	no_session        no such session, or it cannot be resumed
//	session_locked    the session spawned a colony; open a new one
//	queen_unavailable the queen cannot be started or the message delivered
//	invalid_params    malformed params (also -32602 frames)
//	internal          anything else
//
// Method-not-found answers use -32601 and data.kind "invalid_params".
//
// # States
//
// The host keeps the upstream state map: per configured channel "running",
// "off" (disabled), or "failed" with an error string. GET /api/channels serves
// it unchanged. A plugin that dies after starting turns failed; a channel that
// fails to start never affects its siblings. Channels start when serve
// announces the listener and stop (process first, then subscriptions) before
// the server shuts down.
//
// Parity: framework/server/channels.py::ChannelHost
// Parity: framework/server/channels.py::QueenOutput
// Parity: framework/server/channels.py::start_channels
// Parity: framework/server/channels.py::stop_channels
// Parity: framework/server/channels.py::handle_list_channels
// Parity-exempt: the stdio JSON-lines transport, Go command extension, frame
// limits, bounded drop-oldest queue and seq gap reporting are Go-only.
package channels
