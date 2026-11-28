#!/usr/bin/env python3
"""Generate the go_hive LAN discovery + federated queen mail design chain.

The DATA object is the source of truth. Outputs:
  - go_hive/docs/lan-pipes-design.html
  - go_hive/docs/lan-pipes-design.json

This is a design artifact for a post-1.0 optional capability. It does not
claim that the feature or any of the proposed routes exist at HEAD.
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "lan-pipes-design.html"
OUT_JSON = REPO / "go_hive" / "docs" / "lan-pipes-design.json"
ESC = lambda value: html.escape(str(value), quote=True)


DATA = {'schema_version': 1,
 'feature': 'lan-pipes',
 'status': 'design',
 'wan_extension': 'go_hive/docs/wan-peers-design.html',
 'decision': 'Port only the LAN Pipes discovery pattern into go_hive as an optional LAN queen-mail federation. Two '
             'Hive instances on the same private LAN announce themselves over signed UDP multicast, auto-pair when '
             'they share group_id and the group key from group_key_env, and exchange queen mail over HTTPS/JSON. The '
             'existing queen mail engine remains the only mail engine: thread sessions, reply watcher, max_depth, '
             'daily_limit and mail.jsonl audit are reused. There is no remote session control, no raw prompt path, no '
             'second role model and no public/WAN discovery. The schema exposes only five keys; federation, expose, '
             'peers, auto_connect, targets and the rest are deliberately absent, not left at defaults.',
 'goal_beyond_request': 'Let queens on two Hive instances on the same private LAN write to each other with zero peer '
                        'configuration beyond a shared group id and group key, while preserving local thread sessions, '
                        'asynchronous replies, mail limits and audit. The peer layer is a transport and admission '
                        'adapter for queen mail, not a second orchestration model.',
 'feasibility': {'verdict': '发现和信件传输可移植；远程会话控制不在本设计范围。',
                 'why': 'UDP multicast and HTTPS/JSON are standard-library work in Go. The hard part is identity and '
                        'admission, not the socket: queen mail is already asynchronous and owns thread sessions, reply '
                        'watchers, depth, daily limits and audit. The peer layer must feed that engine without '
                        'bypassing participant checks, and a shared group key must not become an internet-wide bearer '
                        'secret.',
                 'reusable_parts': ['UDP multicast discovery and announce freshness are transport patterns that port '
                                    'to Go without cgo.',
                                    'HTTPS/JSON request-response, TLS pinning, retries and expiry are standard library '
                                    'work.',
                                    'Queen mail already has thread sessions, reply delivery, wake-up, max_depth, '
                                    'daily_limit and mail.jsonl audit; the peer layer only supplies remote '
                                    'correspondents and a remote deliverer.',
                                    'The existing queen mail tools already give queens list_queens and message_queen; '
                                    'remote addresses can be added without a new model tool.'],
                 'not_portable': ['The unauthenticated TCP/NDJSON transport and main/sub/master/slave roles from LAN '
                                  'Pipes do not fit Hive.',
                                  'Raw prompt routing and remote session control would bypass queen/colony runtime, '
                                  'permission boundaries and audit.',
                                  'Multicast cannot cross the public internet, and a group key is not a public-network '
                                  'identity.',
                                  'Static peer rosters and manual per-peer tokens are intentionally removed from this '
                                  'design; they are not a fallback.']},
 'problem': {'statement': 'Queen mail is process-local today: list_queens reads local queens, message_queen delivers '
                          'into a local thread session, and the reply watcher writes back to a local writer. Two Hive '
                          'instances on the same LAN have no discovery, no peer identity and no way to address a '
                          'remote queen without custom HTTP calls.',
             'user_value': ['A node joins a LAN group with only group_id and a group key env var; existing nodes need '
                            'no edit.',
                            'Remote queens appear automatically as queen_b@hive-office-b@office-lan and are writable '
                            'with message_queen.',
                            'Replies use the existing asynchronous queen mail path: the remote queen answers in her '
                            'thread session, and the adapter sends the reply back.',
                            'The surface stays mail-only: no remote session create/chat/events, no colony or worker '
                            'control, no new model tool.',
                            'The feature is opt-in and disabled by default; when disabled it opens no UDP or TCP '
                            'socket.'],
             'why_not_now': 'This is an additive Go-only capability for a private LAN, not an upstream Hive feature. '
                            'It remains design-only until the five-key schema, group auto-pair, the queenmail resolver '
                            'boundary and a real two-process LAN E2E are accepted.'},
 'protocol': {'name': 'Hive LAN peer mail',
              'transport': 'HTTPS/JSON over TCP for handshake, mail and health; UDP multicast only for discovery',
              'peer_route': '/peer/v1/qmail',
              'discovery': 'Fixed UDP multicast 239.255.71.67:7101; no static peer roster',
              'auth': 'Group key admission plus per-node ed25519 identity; TLS certificate pinned',
              'framing': 'HTTP JSON request/response; no raw TCP/NDJSON, no WebSocket/SSE',
              'scope': 'Same private LAN, flat peer group; queen-initiated mail chains bounded to 4 hops; post-1.0 '
                       'optional extension'},
 'requirements': [{'id': 'R1',
                   'priority': 'P0',
                   'statement': 'Keep the feature opt-in and default off: lan_peers.enabled gates all discovery '
                                'sockets and peer routes; no parity claim, no default model tool and no change to the '
                                'frozen 1.0 surface.',
                   'acceptance': 'With lan_peers.enabled false or absent, no UDP socket, TCP listener, peer route or '
                                 'model tool is registered; existing local queen mail tests are unchanged.',
                   'traces': ['D1', 'A1', 'A2']},
                  {'id': 'R2',
                   'priority': 'P0',
                   'statement': 'Scope the peer surface to federated queen mail only. Do not add /peer/v1/sessions, '
                                'remote chat/events/delete, colony or worker control, or a second execution engine.',
                   'acceptance': 'The only peer routes are handshake, qmail and health; a request for '
                                 '/peer/v1/sessions returns 404; no remote session is created by the feature.',
                   'traces': ['D1', 'A2']},
                  {'id': 'R3',
                   'priority': 'P0',
                   'statement': 'Discover peers with signed UDP multicast. The announce carries instance identity, '
                                'endpoint, group id and a group proof, but never a bearer token and never a session.',
                   'acceptance': 'A forged or stale announce cannot create a peer; a valid group proof and node '
                                 'signature are required; announce data alone never delivers mail.',
                   'traces': ['D2', 'D3', 'A3']},
                  {'id': 'R4',
                   'priority': 'P0',
                   'statement': 'Fix group auto-pair: no peers list, no auto_connect key, no targets. Same group_id '
                                'plus the group key in group_key_env means automatic pairing and connection; a missing '
                                'key fails closed at startup.',
                   'acceptance': 'Two nodes with the same group_id and key pair without any peer entry; different '
                                 'group ids or keys do not connect; lan_peers.enabled without the env var fails '
                                 'startup; peers, auto_connect, targets and expose are rejected as unknown keys.',
                   'traces': ['D2', 'D3', 'D5', 'A4']},
                  {'id': 'R5',
                   'priority': 'P0',
                   'statement': 'Generate identity automatically: a stable instance_id and an ed25519 keypair on first '
                                'run, persisted under HIVE_HOME/lan_peers; pin a peer public key from its signed '
                                'announce and quarantine a changed key.',
                   'acceptance': 'No identity key is required in configuration; restarts keep the same instance_id and '
                                 'key; a changed public key is quarantined and does not silently replace the pin.',
                   'traces': ['D3', 'A5']},
                  {'id': 'R6',
                   'priority': 'P0',
                   'statement': 'Use HTTPS over TCP for handshake, mail and health, with a self-signed certificate '
                                'pinned from the signed announce. Keep the transport on private LAN addresses only; no '
                                'raw TCP, no NDJSON and no public/WAN target.',
                   'acceptance': 'Non-loopback peers require TLS; a public or non-private URL is refused; no letter '
                                 'body is sent over plaintext; no raw TCP or SSE listener is opened.',
                   'traces': ['D4', 'A6']},
                  {'id': 'R7',
                   'priority': 'P0',
                   'statement': 'Reuse the existing queen mail engine: Mail.Send, Mail.DeliverReply, thread session '
                                'lookup/creation, reply watcher, max_depth, daily_limit and mail.jsonl audit stay '
                                'authoritative. Add only a correspondent resolver and a remote deliverer at the edges.',
                   'acceptance': 'No second mail queue or reply watcher is introduced; local queen mail tests still '
                                 'pass; a remote letter reaches the same thread-session and reply path as a local '
                                 'letter.',
                   'traces': ['D6', 'D7', 'A7']},
                  {'id': 'R8',
                   'priority': 'P0',
                   'statement': 'Address remote queens with the three-level form queen_name@peer_node_id@group_id. '
                                'list_queens merges remote entries from paired peers using the same id, name and title '
                                'fields as local entries; message_queen accepts the address without a manual target '
                                'list. Existing claude_code: conversation refs keep their branch; the resolver checks '
                                'that prefix first.',
                   'acceptance': "A paired peer's queens appear with the same id/name/title shape as local entries; a "
                                 'three-part address routes to the remote queen; a one-, two- or four-part address, a '
                                 'group mismatch, or an offline/unknown peer returns a typed error instead of a silent '
                                 'local lookup failure. A claude_code:<queen>:<session> reply still goes to Claude '
                                 'Code, not the remote parser.',
                   'traces': ['D7', 'A8']},
                  {'id': 'R9',
                   'priority': 'P0',
                   'statement': 'Expose only local queens listed by queen_mail.queens (or all when omitted). Inbound '
                                'mail is always enabled when lan_peers is enabled; there is no expose, federation or '
                                'per-peer allowlist key.',
                   'acceptance': 'A queen outside queen_mail.queens is neither advertised nor writable; an unpaired '
                                 'peer cannot deliver mail; no expose/federation key is accepted.',
                   'traces': ['D9', 'A9']},
                  {'id': 'R10',
                   'priority': 'P1',
                   'statement': 'Carry a bounded letter envelope: letter_id, trace_id, hops, group_id, from_peer, '
                                'from_queen, to_peer, to_queen, text, reply_to, ts and signature. Enforce idempotency '
                                'by letter_id and a fixed max_hops of 4; keep local max_depth independent. Each hop is '
                                'a queen-initiated message or reply; nothing is forwarded automatically.',
                   'acceptance': 'A repeated letter_id is delivered once; a chain beyond 4 cross-instance hops is '
                                 'rejected and audited; local max_depth still applies to the local thread. A received '
                                 'letter is not forwarded to a third peer without a queen action.',
                   'traces': ['D8', 'A10']},
                  {'id': 'R11',
                   'priority': 'P1',
                   'statement': 'Make failures explicit and bounded: offline queue with 24h TTL, retry on 5xx/timeout, '
                                'no retry on 4xx except 429, per-peer rate limits, and audit rows that identify peer, '
                                'letter and outcome without logging bodies or secrets.',
                   'acceptance': 'A peer that disappears mid-send leaves a queued letter that retries or expires with '
                                 'an audit row; duplicates are suppressed; logs and audit contain no group key, token '
                                 'or letter body.',
                   'traces': ['D5', 'D9', 'A11']},
                  {'id': 'R12',
                   'priority': 'P1',
                   'statement': 'Verify with fake peers and a real two-process loopback/LAN E2E; a missing real '
                                'multi-machine run remains an external gap, not PASS. Keep CGO_ENABLED=0 and the '
                                'generator --check gate.',
                   'acceptance': 'Fake-peer tests cover discovery, auth, qmail and idempotency; two processes on one '
                                 'host or LAN exchange a letter and a reply; multi-machine is recorded separately; '
                                 'generator --check passes.',
                   'traces': ['D10', 'A12']}],
 'non_goals': ['Do not add remote session create/chat/events/delete or any /peer/v1/sessions route.',
               'Do not expose colony workers, task queues or tool permissions to a peer.',
               'Do not copy the opcteam main/sub/master/slave role model.',
               'Do not support WAN, public discovery, automatic forwarding/relay trees or unbounded multi-hop '
               'cascading.',
               'Do not add federation, expose, peers, auto_connect, targets, tls, identity or discovery tuning keys.',
               'Do not add a new default model tool or a colony-specific UI.',
               'Do not reuse HIVE_DESKTOP_TOKEN as a peer credential.'],
 'assumptions': ['Both peers run hive serve on the same private LAN and can reach each other on a private IPv4 '
                 'address.',
                 'Both peers set the same group_id and provide the same group key through group_key_env.',
                 'The LAN forwards UDP multicast 239.255.71.67:7101; if it does not, peer discovery is unavailable in '
                 'v1.',
                 'Each node can generate and persist an instance_id and ed25519 keypair under HIVE_HOME/lan_peers.',
                 'Queen mail is already enabled locally; lan_peers only federates it.',
                 'A real multi-machine run is required before calling the transport production-ready.'],
 'options': [{'id': 'O1',
              'name': 'Signed UDP discovery + HTTPS queen-mail federation over the existing Mail engine',
              'verdict': 'chosen',
              'reason': 'Zero peer configuration for a trusted LAN; reuses thread sessions, reply watcher, limits and '
                        'audit; no second engine; no remote session surface.'},
             {'id': 'O2',
              'name': 'Literal LAN Pipes NDJSON TCP protocol and main/sub/master/slave roles',
              'verdict': 'rejected',
              'reason': 'Unauthenticated TCP, raw prompt routing and a second role model; bypasses queen mail and Hive '
                        'runtime boundaries.'},
             {'id': 'O3',
              'name': 'Remote session control over the existing /api/sessions API',
              'verdict': 'rejected',
              'reason': 'Out of scope for this feature: it needs session lifecycle, SSE, permission relay and a target '
                        'allowlist, which the user explicitly removed.'},
             {'id': 'O4',
              'name': 'Static peer roster with per-peer tokens',
              'verdict': 'rejected',
              'reason': 'The user removed peers, tokens and targets; the fixed group_id + group_key pair is the only '
                        'admission model.'},
             {'id': 'O5',
              'name': 'Extend internal/channels with LAN peers',
              'verdict': 'rejected',
              'reason': 'Channels adapt external chat platforms to sessions; they are not a peer discovery or '
                        'queen-mail admission protocol.'},
             {'id': 'O6',
              'name': 'Direct public HTTPS with mTLS or a VPN/relay',
              'verdict': 'separate',
              'reason': 'Internet support needs a different discovery, NAT and identity model. It is a separate '
                        'deployment/design, not a lan_peers config switch.'}],
 'design': {'D1': {'title': 'Opt-in and mail-only surface',
                   'text': 'lan_peers.enabled gates the UDP discovery socket and the peer routes. When it is false or '
                           'absent, no socket or route exists. The feature requires queen_mail.enabled; there is no '
                           'peer session route, no /peer/v1/sessions, no remote chat/events/delete and no colony or '
                           'worker control.'},
            'D2': {'title': 'Fixed group discovery',
                   'text': 'Discovery is UDP multicast on 239.255.71.67:7101, TTL 1, announce every 5 seconds and '
                           'expire after 15 seconds. The announce carries instance_id, name, url, public_key, '
                           'group_id, capabilities, ts, a group proof and a node signature. There is no peers list, no '
                           'auto_connect key, no discovery.enabled key and no static fallback; the only discovery '
                           'configuration is group_id and group_key_env.'},
            'D3': {'title': 'Identity and group admission',
                   'text': 'On first run, generate an instance_id and an ed25519 keypair under HIVE_HOME/lan_peers. '
                           'The display name defaults to the hostname; it is not an address key and not a trust '
                           'source. The announce includes group_proof = HMAC(group_key, instance_id || public_key || '
                           'ts) and a node signature. A receiver checks group_id, proof, signature and timestamp, then '
                           'pins the public key. A changed key is quarantined, not silently accepted. If group_key_env '
                           'is missing while lan_peers.enabled is true, startup fails closed.'},
            'D4': {'title': 'HTTPS transport',
                   'text': "Handshake, mail and health use HTTPS over TCP on the peer's advertised private address. "
                           "The self-signed certificate uses the node's ed25519 identity; the peer pins the public key "
                           'from the signed announce, not the leaf bytes. Non-loopback peers must use TLS. Bodies are '
                           'bounded JSON; there is no raw TCP, no NDJSON and no WebSocket/SSE. The peer listener only '
                           'serves /peer/v1/handshake, /peer/v1/qmail and /peer/v1/health.'},
            'D5': {'title': 'Connection lifecycle',
                   'text': 'A peer moves through discovered, paired and connected states. A valid announce auto-pairs; '
                           'a successful handshake connects; a missed announce or health probe marks it offline. A '
                           'changed IP is accepted only from a signed announce. Group-key rotation requires updating '
                           'the env var and restarting all nodes; there is no online rotation in v1.'},
            'D6': {'title': 'Reuse the queen mail engine',
                   'text': 'Mail.Send, Mail.DeliverReply, thread session lookup/creation, reply watcher, max_depth, '
                           'daily_limit and mail.jsonl audit stay authoritative. The peer layer does not create a '
                           'second queue, reply watcher or session store. A remote letter enters the same '
                           'thread-session and reply path as a local letter.'},
            'D7': {'title': 'Correspondence resolver',
                   'text': 'Add a resolver at the edge of queen mail: local queens come from queen_mail.queens and the '
                           'queens directory; remote queens come from paired peers and expire with the peer roster. '
                           'list_queens merges both and returns exactly the existing {id, name, title} shape; a remote '
                           'id is the full three-level address queen_name@peer_node_id@group_id. The resolver checks '
                           'ref types in this order: (1) the existing claude_code: prefix via '
                           'ConversationPrefix/IsConversationRef, which stays unchanged and is handled first; (2) '
                           'exactly three @-separated segments for a remote queen, with group_id equal to the local '
                           'discovery.group_id; (3) a local queen id from the roster; (4) a local session or reply '
                           'ref. ConversationPrefix and QueenIDOfRef are not changed; remote addresses never use that '
                           'prefix. message_queen parses exactly three @-separated segments: queen_name (the queen '
                           "id), peer_node_id (the peer's stable instance_id, not its display name) and group_id (must "
                           'equal the local discovery.group_id). One, two or four segments, an @ inside a segment, or '
                           "a group mismatch is a typed error. A remote thread's meta records the full writer address "
                           'so replies route back. Mail.Send target checks and DeliverReply writer checks use the '
                           'resolver instead of a local-only queens map.'},
            'D8': {'title': 'Letter envelope and loops',
                   'text': 'The wire letter carries letter_id, trace_id, hops, group_id, from_peer, from_queen, '
                           'to_peer, to_queen, text, reply_to, ts and signature. letter_id is idempotent for 24 hours. '
                           'max_hops is fixed at 4 across instances; local max_depth remains independent. A letter '
                           'beyond max_hops is rejected and audited, never silently delivered. A hop is a deliberate '
                           'queen message_queen or reply, never automatic forwarding; there is no store-and-forward '
                           'relay. max_hops only bounds the queen-initiated chain.'},
            'D9': {'title': 'Exposure and security',
                   'text': 'Only queens listed by queen_mail.queens (or all local queens when omitted) are advertised '
                           'and writable. The advertised roster carries only the existing id, name and title fields; '
                           'private profile sections are never exposed. Inbound mail is always enabled when lan_peers '
                           'is enabled; there is no expose or federation key. The group key is admission only; each '
                           'node has its own ed25519 identity. Private LAN only, TLS pinned, bounded bodies, per-peer '
                           'rate limits, and no group key, token or letter body in logs or announce.'},
            'D10': {'title': 'Package boundary and verification',
                    'text': 'Put discovery, identity, transport and the qmail client/server in internal/lanpeer; keep '
                            'the server adapter thin; add only a correspondent resolver and remote deliverer interface '
                            'to internal/queenmail. Do not import internal/server, internal/cli or '
                            'internal/tools/builtin from internal/lanpeer. No new model tool or UI. Verify with fake '
                            'peers and a real two-process loopback/LAN E2E; keep CGO_ENABLED=0 and generator '
                            '--check.'}},
 'config_contract': {'status': 'proposed / absent at HEAD',
                     'config': 'lan_peers + queen_mail',
                     'authority': 'Only lan_peers is new; queen_mail.enabled and queen_mail.queens are existing keys. '
                                  'federation, expose, peers, auto_connect, targets, tls, identity and '
                                  'discovery.enabled/group/port/interface are not part of the schema and must be '
                                  'rejected as unknown keys.',
                     'fields': [{'name': 'lan_peers.enabled',
                                 'required': 'no',
                                 'meaning': 'default false; true opens the UDP discovery socket and peer routes; '
                                            'requires queen_mail.enabled and group_key_env'},
                                {'name': 'lan_peers.discovery.group_id',
                                 'required': 'yes when enabled',
                                 'meaning': 'non-secret logical group name; announces from another group are ignored '
                                            "and a remote address's third segment must equal it"},
                                {'name': 'lan_peers.discovery.group_key_env',
                                 'required': 'yes when enabled',
                                 'meaning': 'environment variable name holding the shared group key; the value is '
                                            'never logged or announced'},
                                {'name': 'queen_mail.enabled',
                                 'required': 'no',
                                 'meaning': 'existing switch; must be true for local or federated queen mail'},
                                {'name': 'queen_mail.queens',
                                 'required': 'no',
                                 'meaning': 'local queens that participate and are advertised; omitted means all local '
                                            'queens, empty array means none'}],
                     'sample': {'lan_peers': {'enabled': True,
                                              'discovery': {'group_id': 'office-lan',
                                                            'group_key_env': 'HIVE_DISCOVERY_GROUP'}},
                                'queen_mail': {'enabled': True, 'queens': ['queen_a', 'queen_b']}},
                     'reload': 'Read at serve startup; changing enabled, group_id, group_key_env or queen_mail.queens '
                               'requires a restart in v1.'},
 'protocol_contract': {'sequence': ['Both nodes start with lan_peers.enabled and the same group_id plus group_key_env '
                                    'value; each generates instance_id and ed25519 identity.',
                                    'Each node sends a signed UDP announce; the receiver verifies group proof, '
                                    'signature and timestamp, then auto-pairs and pins the public key.',
                                    'The peer client posts /peer/v1/handshake over HTTPS; a challenge-response '
                                    'confirms both node keys and returns the peer queen roster with the same id, name '
                                    'and title fields as local queens.',
                                    'The ref resolver checks the existing claude_code: prefix first, then the '
                                    'three-level remote address, then a local queen id or session ref.',
                                    'list_queens merges local and remote entries; the queen writes to '
                                    'queen_b@hive-office-b@office-lan with message_queen.',
                                    'The sender posts the letter to /peer/v1/qmail; the receiver returns 202 accepted '
                                    'and injects it into the local thread session.',
                                    'The remote queen answers in her thread session; the reply watcher posts a '
                                    "separate letter back to the writer's /peer/v1/qmail.",
                                    'A queen may reply to the sender or deliberately write to another peer; each leg '
                                    'increments hops, and nothing is forwarded automatically.',
                                    'An offline peer queues the letter for up to 24 hours; expiry, duplicate '
                                    'suppression and hop limits are audited.'],
                       'routes': ['POST /peer/v1/handshake', 'POST /peer/v1/qmail', 'GET /peer/v1/health'],
                       'announce': {'proto': 'hive-lan-peer/1',
                                    'fields': ['instance_id',
                                               'name',
                                               'url',
                                               'public_key',
                                               'group_id',
                                               'capabilities',
                                               'ts',
                                               'group_proof',
                                               'signature'],
                                    'rule': 'announce proves group admission and node identity; it carries no bearer '
                                            'token and creates no session'},
                       'letter_event': {'kind': 'queen_mail',
                                        'fields': ['letter_id',
                                                   'trace_id',
                                                   'hops',
                                                   'group_id',
                                                   'from_peer',
                                                   'from_queen',
                                                   'to_peer',
                                                   'to_queen',
                                                   'text',
                                                   'reply_to',
                                                   'ts',
                                                   'signature'],
                                        'response': '202 accepted means the letter entered the local mail path; the '
                                                    "queen's reply is a separate POST"},
                       'ref_kinds': [{'ref': 'claude_code:<queen>:<session>',
                                      'owner': 'Claude Code queen conversation',
                                      'handling': 'Existing ConversationPrefix / IsConversationRef branch; strip the '
                                                  "prefix, split on the last ':' for the queen id; unchanged."},
                                     {'ref': 'queen_name@peer_node_id@group_id',
                                      'owner': 'Remote Hive queen',
                                      'handling': 'Exactly three @-separated segments; group_id must equal '
                                                  'discovery.group_id; resolved before Mail.Send participant checks.'},
                                     {'ref': 'queen_id',
                                      'owner': 'Local Hive queen',
                                      'handling': 'Local roster from queen_mail.queens and the queens directory.'},
                                     {'ref': 'session_<id>',
                                      'owner': 'Local session / reply target',
                                      'handling': 'SessionManager plus thread meta.json reply_to.'}]},
 'failure_modes': [{'case': 'lan_peers.enabled false or absent',
                    'result': 'no UDP socket, no TCP peer route and no model tool'},
                   {'case': 'group_key_env missing while enabled',
                    'result': 'startup fails closed; no anonymous discovery'},
                   {'case': 'different group_id or wrong group key',
                    'result': 'announce is ignored; no peer record and no mail'},
                   {'case': 'forged or replayed announce',
                    'result': 'signature, proof or timestamp check fails; no peer is created'},
                   {'case': 'peer public key changed',
                    'result': 'quarantine the peer; do not replace the pin silently'},
                   {'case': 'non-private or WAN URL', 'result': '403/refused; transport stays on the private LAN'},
                   {'case': 'TLS certificate mismatch', 'result': 'connection refused; no letter body is sent'},
                   {'case': 'duplicate letter_id', 'result': 'idempotent 202/200; only one delivery'},
                   {'case': 'letter chain over max_hops', 'result': 'reject and audit dropped=max_hops'},
                   {'case': 'peer offline', 'result': 'queue up to 24h, retry 5xx/timeout, then audit an expired drop'},
                   {'case': 'oversized body or too many peers',
                    'result': '413 or bounded registry error; no unbounded memory'},
                   {'case': 'unknown config key', 'result': 'startup error; removed keys are not silently ignored'},
                   {'case': 'internet/public exposure',
                    'result': 'out of scope for this LAN profile; see docs/wan-peers-design.html for the separate rendezvous/relay design'}],
 'limits': {'max_peers': '32 per instance',
            'announce_bytes': '4 KiB',
            'qmail_body': '32 KiB',
            'queue_ttl': '24 hours',
            'max_hops': 'fixed 4 across instances',
            'announce_interval': '5 seconds; expire after 15 seconds',
            'handshake_timeout': '5 seconds',
            'qmail_timeout': '10 seconds',
            'per_peer_rate': '60 letters per minute inbound',
            'idempotency_window': '24 hours',
            'tls': 'required for non-loopback peer addresses',
            'remote_sessions': 'none; this feature has no session surface'},
 'acceptance': [{'id': 'A1',
                 'test': 'Feature gate',
                 'pass': 'disabled or absent config registers no UDP socket, peer route or tool; existing local suite '
                         'is unchanged.'},
                {'id': 'A2',
                 'test': 'Mail-only surface',
                 'pass': 'only handshake, qmail and health routes exist; /peer/v1/sessions returns 404; no remote '
                         'session is created.'},
                {'id': 'A3',
                 'test': 'Discovery and admission',
                 'pass': 'a valid signed announce creates a candidate; forged, stale or wrong-group announce does not; '
                         'announce alone delivers no mail.'},
                {'id': 'A4',
                 'test': 'Fixed group auto-pair',
                 'pass': 'same group_id/key pairs with no peers entry; different group/key does not; missing env fails '
                         'startup; peers, auto_connect, targets and expose are rejected.'},
                {'id': 'A5',
                 'test': 'Identity pinning',
                 'pass': 'instance_id and ed25519 key persist across restart; a changed public key is quarantined.'},
                {'id': 'A6',
                 'test': 'Transport boundary',
                 'pass': 'non-loopback requires TLS; public/non-private URL is refused; no plaintext body, raw TCP or '
                         'SSE.'},
                {'id': 'A7',
                 'test': 'Queen mail reuse',
                 'pass': 'no second mail engine; local tests pass; a remote letter uses the same thread session and '
                         'reply watcher.'},
                {'id': 'A8',
                 'test': 'Remote address',
                 'pass': 'list_queens shows queen_b@hive-office-b@office-lan with the same id/name/title shape as '
                         'local entries; message_queen routes it; a malformed address or offline/unknown peer returns '
                         'a typed error.'},
                {'id': 'A9',
                 'test': 'Exposure',
                 'pass': 'only queen_mail.queens is advertised; an unpaired peer cannot deliver; no expose or '
                         'federation key is accepted.'},
                {'id': 'A10',
                 'test': 'Envelope and loops',
                 'pass': 'duplicate letter_id delivers once; hops over 4 is rejected and audited; local max_depth '
                         'still applies. No letter is forwarded to a third peer without a queen action.'},
                {'id': 'A11',
                 'test': 'Reliability and audit',
                 'pass': 'offline queue retries/expires with audit; 4xx is not retried except 429; logs contain no '
                         'key, token or body.'},
                {'id': 'A12',
                 'test': 'E2E and drift',
                 'pass': 'fake peer plus two-process loopback/LAN exchange a letter and reply; CGO=0 build passes; '
                         'generator --check returns 0.'}],
 'review_focus': [{'input': 'forged announce advertises a fake endpoint',
                   'expected': 'proof/signature fails; no peer record and no mail',
                   'owner': 'T3'},
                  {'input': 'remote peer posts /peer/v1/sessions',
                   'expected': '404; the mail-only surface has no session route',
                   'owner': 'T6'},
                  {'input': 'address has one, two or four segments',
                   'expected': 'typed error; no remote lookup or delivery',
                   'owner': 'T5'},
                  {'input': 'same letter_id arrives twice',
                   'expected': 'one delivery; 202/200 idempotent response',
                   'owner': 'T5'},
                  {'input': 'peer key changes after pairing',
                   'expected': 'quarantine; no silent replacement of the pin',
                   'owner': 'T2'},
                  {'input': 'letter chain exceeds 4 hops',
                   'expected': 'reject and audit dropped=max_hops',
                   'owner': 'T5'},
                  {'input': 'group key rotated on only one node',
                   'expected': 'that node stops pairing; no partial trust',
                   'owner': 'T3'},
                  {'input': 'queen_mail disabled while lan_peers enabled',
                   'expected': 'startup error; no dead peer socket',
                   'owner': 'T1'},
                  {'input': 'remote roster grows a skills, description or remote flag',
                   'expected': 'reject; only the existing id, name and title fields are returned',
                   'owner': 'T5'},
                  {'input': 'custom queen has an empty title',
                   'expected': 'return id and name; do not invent a new description field',
                   'owner': 'T5'},
                  {'input': 'claude_code: ref reaches the remote parser',
                   'expected': 'ConversationPrefix branch wins; Claude Code reply path unchanged',
                   'owner': 'T5'},
                  {'input': 'a received letter is relayed to a third peer',
                   'expected': 'only as a new deliberate message_queen with trace_id/hops; never automatic; max 4',
                   'owner': 'T5'}],
 'tasks': [{'id': 'T1',
            'depends_on': [],
            'writes': ['internal/lanpeer/config.go', 'internal/lanpeer/config_test.go'],
            'done': 'five-key schema validates; removed keys are rejected; missing env and queen_mail dependency fail '
                    'startup.'},
           {'id': 'T2',
            'depends_on': ['T1'],
            'writes': ['internal/lanpeer/identity.go', 'internal/lanpeer/identity_test.go'],
            'done': 'instance_id and ed25519 identity persist; pinning, key-change quarantine and restart pass.'},
           {'id': 'T3',
            'depends_on': ['T1'],
            'writes': ['internal/lanpeer/discovery.go', 'internal/lanpeer/discovery_test.go'],
            'done': 'multicast announce, group proof, signature, timestamp, expiry, interface selection and replay '
                    'rejection pass.'},
           {'id': 'T4',
            'depends_on': ['T2', 'T3'],
            'writes': ['internal/lanpeer/transport.go',
                       'internal/lanpeer/transport_test.go',
                       'internal/lanpeer/client.go'],
            'done': 'self-signed TLS pinning, handshake, qmail and health work; body limits and private-address checks '
                    'pass.'},
           {'id': 'T5',
            'depends_on': ['T4'],
            'writes': ['internal/queenmail/correspondent.go',
                       'internal/queenmail/correspondent_test.go',
                       'internal/queenmail/mail.go',
                       'internal/queenmail/mail_test.go',
                       'internal/cli/session_tools_queen_mail.go',
                       'internal/cli/session_tools_queen_mail_test.go',
                       'internal/server/routes_queens_mail_test.go',
                       'docs/queen-mail.md'],
            'done': 'resolver, remote deliverer, letter envelope, idempotency and max_hops pass; local and remote '
                    'list_queens entries keep the same id/name/title shape; local mail behavior is unchanged; '
                    'claude_code: refs still resolve to the Claude Code branch.'},
           {'id': 'T6',
            'depends_on': ['T5'],
            'writes': ['internal/server/lanpeer_adapter.go', 'internal/server/lanpeer_adapter_test.go'],
            'done': 'thin server wiring, qmail route, peer roster and audit work; no session route and no '
                    'desktop-token reuse.'},
           {'id': 'T7',
            'depends_on': ['T6'],
            'writes': ['internal/lanpeer/e2e_test.go', 'docs/lan-pipes-e2e.md'],
            'done': 'fake peer plus two-process loopback/LAN letter and reply pass; CGO=0, full CI and generator '
                    '--check pass.'}],
 'risks': [{'risk': 'Group key compromise',
            'impact': 'any holder can join the LAN group',
            'mitigation': 'keep the key in env only, rotate by restarting all nodes, never log or announce it; use a '
                          'VPN for untrusted networks.'},
           {'risk': 'Multicast blocked or interfaces ambiguous',
            'impact': 'no peers are discovered',
            'mitigation': 'document the limitation; v1 has no static peer fallback; use a VPN/overlay that carries '
                          'multicast or a separate design.'},
           {'risk': 'No per-peer revocation',
            'impact': 'a member cannot be removed individually',
            'mitigation': 'rotate the group key; per-node identity prevents impersonation but not admission.'},
           {'risk': 'Changed node key',
            'impact': 'a legitimate node is quarantined',
            'mitigation': 'remove its runtime peer record and restart, or rotate the group key; log a clear operator '
                          'message.'},
           {'risk': 'Asynchronous mail latency',
            'impact': 'no immediate reply; offline peers delay delivery',
            'mitigation': 'keep the 24h queue, retry policy and explicit offline/expired errors.'},
           {'risk': 'Duplicate or looping letters',
            'impact': 'duplicate replies or endless A-B chains',
            'mitigation': 'letter_id idempotency, trace_id and fixed max_hops=4.'},
           {'risk': 'Scope creep into session control',
            'impact': 'reintroduces targets, SSE, permissions and a second surface',
            'mitigation': 'mail-only requirement R2, no /peer/v1/sessions, no expose/targets keys.'},
           {'risk': 'No real multi-machine run',
            'impact': 'LAN behavior differs on real networks',
            'mitigation': 'two-process test is required; record multi-machine as an external gap, not PASS.'}],
 'open_questions': ['Group-key rotation is restart-only in v1; is an online rotation endpoint needed later, or is '
                    'restart acceptable?',
                    'A changed node public key is quarantined; should recovery be deleting its runtime peer record, '
                    'rotating the group key, or both?',
                    'Remote attachments remain path text only in v1; is cross-instance file transfer required, or '
                    'explicitly out of scope?',
                    'Inbound letters do not count against queen_mail.daily_limit; should they consume a separate '
                    'per-peer quota?',
                    'When queen_mail.queens is omitted, all local queens are advertised; should an explicit list be '
                    'required for safer defaults?',
                    'If the separate WAN design (docs/wan-peers-design.html) is adopted, should LAN and WAN '
                    'share the ed25519 key/fingerprint storage, or keep separate identities?'],
 'source_refs': [{'label': 'LAN Pipes user guide',
                  'path': 'codes/bytecoder/docs/features/lan-pipes.md',
                  'use': 'discovery, announce, peer list and the pattern this design borrows'},
                 {'label': 'queen mail docs',
                  'path': 'go_hive/docs/queen-mail.md',
                  'use': 'thread sessions, delivery, reply watcher, max_depth, daily_limit, audit'},
                 {'label': 'queen mail engine',
                  'path': 'go_hive/go-hive/internal/queenmail',
                  'use': 'Mail.Send, Mail.DeliverReply, Letter and config; the engine this feature reuses'},
                 {'label': 'queen mail adapter',
                  'path': 'go_hive/go-hive/internal/server/adapters_qmail.go',
                  'use': 'thread session find/create, reply subscription and delivery; the path to extend'},
                 {'label': 'queen mail tools',
                  'path': 'go_hive/go-hive/internal/cli/session_tools_queen_mail.go',
                  'use': 'list_queens and message_queen surfaces that gain remote addresses'},
                 {'label': 'desktop auth',
                  'path': 'go_hive/go-hive/internal/server/app.go',
                  'use': 'HIVE_DESKTOP_TOKEN; must not be reused as a peer credential'},
                 {'label': 'serve listener',
                  'path': 'go_hive/go-hive/internal/cli/loader.go',
                  'use': 'HTTP listener and non-loopback token refusal; peer listener must stay private'},
                 {'label': 'channel protocol',
                  'path': 'go_hive/go-hive/internal/channels/protocol.go',
                  'use': 'existing channel vocabulary; not used for LAN peer discovery'}]}


def render_html(data: dict) -> str:
    def lis(items):
        return "".join(f"<li>{ESC(x)}</li>" for x in items)

    req_rows = "".join(
        f"<tr><td><code>{ESC(r['id'])}</code></td><td>{ESC(r['priority'])}</td><td>{ESC(r['statement'])}</td>"
        f"<td>{ESC(r['acceptance'])}</td><td>{ESC(', '.join(r['traces']))}</td></tr>"
        for r in data["requirements"]
    )
    opt_rows = "".join(
        f"<tr><td><code>{ESC(o['id'])}</code></td><td>{ESC(o['name'])}</td>"
        f"<td><span class=\"pill {'ok' if o['verdict'] == 'chosen' else 'warn' if o['verdict'] in {'deferred', 'separate'} else 'muted'}\">{ESC(o['verdict'])}</span></td>"
        f"<td>{ESC(o['reason'])}</td></tr>"
        for o in data["options"]
    )
    design_cards = "".join(
        f"<div class=\"card\"><div class=\"k\">{ESC(k)}</div><h3>{ESC(v['title'])}</h3><p>{ESC(v['text'])}</p></div>"
        for k, v in data["design"].items()
    )
    field_rows = "".join(
        f"<tr><td><code>{ESC(f['name'])}</code></td><td>{ESC(f['required'])}</td><td>{ESC(f['meaning'])}</td></tr>"
        for f in data["config_contract"]["fields"]
    )
    config_text = json.dumps(data["config_contract"]["sample"], ensure_ascii=False, indent=2)
    protocol_items = lis(data["protocol_contract"]["sequence"])
    route_items = lis(data["protocol_contract"]["routes"])
    ref_rows = "".join(
        f"<tr><td><code>{ESC(r['ref'])}</code></td><td>{ESC(r['owner'])}</td><td>{ESC(r['handling'])}</td></tr>"
        for r in data["protocol_contract"]["ref_kinds"]
    )
    announce_text = json.dumps(data["protocol_contract"]["announce"], ensure_ascii=False, indent=2)
    letter_text = json.dumps(data["protocol_contract"]["letter_event"], ensure_ascii=False, indent=2)
    failure_rows = "".join(
        f"<tr><td>{ESC(f['case'])}</td><td>{ESC(f['result'])}</td></tr>" for f in data["failure_modes"]
    )
    limit_items = "".join(f"<li><code>{ESC(k)}</code>: {ESC(v)}</li>" for k, v in data["limits"].items())
    acc_rows = "".join(
        f"<tr><td><code>{ESC(a['id'])}</code></td><td>{ESC(a['test'])}</td><td>{ESC(a['pass'])}</td></tr>"
        for a in data["acceptance"]
    )
    review_rows = "".join(
        f"<tr><td>{ESC(r['input'])}</td><td>{ESC(r['expected'])}</td><td><code>{ESC(r['owner'])}</code></td></tr>"
        for r in data["review_focus"]
    )
    task_rows = "".join(
        f"<tr><td><code>{ESC(t['id'])}</code></td><td>{ESC(', '.join(t['depends_on']) or '—')}</td>"
        f"<td><code>{ESC(', '.join(t['writes']) or '—')}</code></td><td>{ESC(t['done'])}</td></tr>"
        for t in data["tasks"]
    )
    risk_rows = "".join(
        f"<tr><td>{ESC(r['risk'])}</td><td>{ESC(r['impact'])}</td><td>{ESC(r['mitigation'])}</td></tr>"
        for r in data["risks"]
    )
    source_items = "".join(
        f"<li><code>{ESC(s['path'])}</code> — {ESC(s['use'])}</li>" for s in data["source_refs"]
    )
    meta = (
        f"<span>状态：<b>{ESC(data['status'])}</b></span>"
        f"<span>需求：<b>{len(data['requirements'])}</b></span>"
        f"<span>设计项：<b>{len(data['design'])}</b></span>"
        f"<span>验收：<b>{len(data['acceptance'])}</b></span>"
        f"<span>范围：<b>{ESC(data['protocol']['scope'])}</b></span>"
    )
    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>lan-pipes · go_hive LAN 发现 + 联邦 queen mail 需求—设计链</title>
<style>
:root{{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}}
*{{box-sizing:border-box}} body{{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}}
a{{color:var(--accent2);text-decoration:none}} a:hover{{text-decoration:underline}}
.wrap{{max-width:1380px;margin:0 auto;padding:44px 26px 80px}}
header{{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}}
.eyebrow{{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}}
h1{{font-size:30px;line-height:1.25;margin:10px 0;letter-spacing:-.02em}} h1 span{{color:var(--accent)}}
.lede{{color:var(--muted);max-width:1120px;font-size:15px}} .meta{{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px}} .meta b{{color:var(--text)}}
section{{margin:38px 0}} h2{{font-size:22px;margin:0 0 8px}} h2 .num{{color:var(--accent);font-size:14px;margin-right:8px}} h3{{font-size:15.5px;margin:16px 0 8px;color:#cfdae6}}
p{{color:#c6d2df;margin:8px 0}} .sub{{color:var(--muted);font-size:12.5px}}
.grid{{display:grid;gap:12px}} .g2{{grid-template-columns:repeat(2,minmax(0,1fr))}} .g3{{grid-template-columns:repeat(3,minmax(0,1fr))}} .grid>*{{min-width:0}}
@media(max-width:1000px){{.g3{{grid-template-columns:repeat(2,minmax(0,1fr))}}}} @media(max-width:560px){{.g3,.g2{{grid-template-columns:1fr}}.wrap{{padding:30px 16px 60px}}h1{{font-size:24px}}}}
.card{{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0}} .card .k{{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700}} .card .d{{color:var(--muted);font-size:12px;margin-top:5px}}
.tablewrap{{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}}
table{{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed}} th,td{{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word}} th{{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1}} tr:hover td{{background:#121a25}} tr:last-child td{{border-bottom:0}}
code{{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}}
pre{{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2}}
.pill{{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal;margin:1px 2px 1px 0}} .pill.ok{{color:var(--ok);border-color:#1d3b26;background:#0f1a13}} .pill.warn{{color:var(--warn);border-color:#3d3013;background:#1a150a}} .pill.muted{{color:var(--muted);border-color:var(--line);background:#0f1620}}
.callout{{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4}} .callout.info{{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}} .callout.bad{{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7}}
ul,ol{{margin:7px 0 7px 20px;padding:0}} li{{margin:4px 0;color:#c6d2df}} footer{{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}}
@media(max-width:560px){{table{{font-size:12px}}th,td{{padding:7px 6px}}pre{{white-space:pre-wrap;overflow-wrap:anywhere}}}}
</style></head><body><div class="wrap">
<header><div class="eyebrow">GO_HIVE · LAN PEERS · POST-1.0 REQUIREMENTS → DESIGN</div>
<h1>LAN Pipes → go_hive <span>LAN 发现 + 联邦 queen mail</span></h1>
<p class="lede">把 LAN Pipes 的发现模式移植到 go_hive，做成可选的局域网 queen mail 联邦：UDP 组播发现、组密钥自动配对、HTTPS/JSON 投递；复用现有 queen mail 引擎，不做远程会话控制。默认关闭，单跳私有 LAN，post-1.0；配置只保留五个键。</p>
<div class="meta">{meta}</div>
<div class="callout info"><b>2026-11-24 范围修订：</b>本页从“远程会话 peer”收敛为“LAN 发现 + 联邦 queen mail”。schema 只保留 <code>lan_peers.enabled</code>、<code>discovery.group_id</code>、<code>discovery.group_key_env</code>、<code>queen_mail.enabled</code>、<code>queen_mail.queens</code> 五个键；<code>federation</code>、<code>expose</code>、<code>peers</code>、<code>auto_connect</code>、<code>targets</code> 不再存在。固定 group 自动配对、<code>inbound=true</code>、<code>max_hops=4</code>，不开放远程会话。</div>
<div class="callout info"><b>2026-11-25 WAN 扩展：</b>公网 A + NAT B/C/D 的 rendezvous/relay/委派发现是独立设计 <a href="wan-peers-design.html">wan-peers-design.html</a>；它不改变本页的 LAN 五键，也不代表公网发现已实现。</div>
</header>

<section id="feasibility"><h2><span class="num">00</span>可行性判断</h2>
<div class="callout bad"><b>结论：</b>{ESC(data['feasibility']['verdict'])} <br><b>原因：</b>{ESC(data['feasibility']['why'])}</div>
<div class="grid g2"><div class="card"><div class="k">可复用</div><ul>{lis(data['feasibility']['reusable_parts'])}</ul></div><div class="card"><div class="k">不能直接搬</div><ul>{lis(data['feasibility']['not_portable'])}</ul></div></div>
<div class="callout info"><b>目标之外：</b>{ESC(data['goal_beyond_request'])}</div>
<p>{ESC(data['problem']['statement'])}</p>
<div class="grid g2"><div class="card"><div class="k">用户价值</div><ul>{lis(data['problem']['user_value'])}</ul></div><div class="card"><div class="k">为什么不是当前 parity</div><div class="d">{ESC(data['problem']['why_not_now'])}</div></div></div>
<h3>非目标</h3><ul>{lis(data['non_goals'])}</ul><h3>前提</h3><ul>{lis(data['assumptions'])}</ul></section>

<section id="requirements"><h2><span class="num">01</span>需求链</h2>
<p class="sub">R1/R2 是范围边界；R3–R5 是发现与身份边界；R6 是传输边界；R7–R10 是 queen mail 复用与信封；R12 是验证边界。</p>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:6%">优先级</th><th style="width:38%">需求</th><th style="width:34%">验收</th><th style="width:16%">链路</th></tr></thead><tbody>{req_rows}</tbody></table></div></section>

<section id="options"><h2><span class="num">02</span>方案决策</h2>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:30%">方案</th><th style="width:9%">结论</th><th style="width:55%">理由</th></tr></thead><tbody>{opt_rows}</tbody></table></div>
<div class="callout"><b>选择：</b>{ESC(data['decision'])}</div></section>

<section id="design"><h2><span class="num">03</span>设计链</h2><div class="grid g2">{design_cards}</div>
<div class="callout info"><b>包边界：</b>discovery/identity/transport/qmail client-server 放在 <code>internal/lanpeer</code>；server 只做薄适配器；<code>internal/queenmail</code> 只加 correspondent/remote deliverer 接口；不得从核心 import <code>internal/server</code>、<code>internal/cli</code> 或 <code>internal/tools/builtin</code>；不增加默认模型工具。</div></section>

<section id="protocol"><h2><span class="num">04</span>通讯与信封</h2>
<div class="grid g3">
<div class="card"><div class="k">发现</div><h3>{ESC(data['protocol']['discovery'])}</h3><p>UDP 组播只产生候选和准入证明，不传信件。</p></div>
<div class="card"><div class="k">投递</div><h3>{ESC(data['protocol']['transport'])}</h3><p>握手、发信、回信、健康检查；没有长连接。</p></div>
<div class="card"><div class="k">信任</div><h3>{ESC(data['protocol']['auth'])}</h3><p>组密钥准入 + 节点身份；announce 不携带 token。</p></div>
</div>
<h3>操作顺序</h3><ol>{protocol_items}</ol>
<h3>peer 路由</h3><ul>{route_items}</ul>
<h3>ref 类型与解析顺序</h3>
<div class="tablewrap"><table><thead><tr><th style="width:34%">Ref</th><th style="width:20%">归属</th><th style="width:46%">解析</th></tr></thead><tbody>{ref_rows}</tbody></table></div>
<h3>announce</h3><pre>{ESC(announce_text)}</pre>
<h3>信件信封</h3><pre>{ESC(letter_text)}</pre>
<div class="callout bad"><b>关键边界：</b>只做 queen mail，不传 raw prompt，不提供 <code>/peer/v1/sessions</code>，不复制 main/sub/master/slave，不把 <code>HIVE_DESKTOP_TOKEN</code> 当 peer 凭证。</div></section>

<section id="config"><h2><span class="num">05</span>配置与身份</h2>
<div class="callout info"><b>配置：</b><code>{ESC(data['config_contract']['config'])}</code> <span class="pill warn">{ESC(data['config_contract']['status'])}</span> <b>归属：</b>{ESC(data['config_contract']['authority'])}</div>
<h3>示例</h3><pre>{ESC(config_text)}</pre>
<h3>字段</h3><div class="tablewrap"><table><thead><tr><th style="width:28%">字段</th><th style="width:22%">必填</th><th style="width:50%">含义</th></tr></thead><tbody>{field_rows}</tbody></table></div>
<p><b>生效：</b>{ESC(data['config_contract']['reload'])}</p></section>

<section id="failure"><h2><span class="num">06</span>失败与限额</h2>
<div class="tablewrap"><table><thead><tr><th style="width:42%">场景</th><th style="width:58%">结果</th></tr></thead><tbody>{failure_rows}</tbody></table></div>
<div class="grid g2"><div class="card"><div class="k">Limits</div><ul>{limit_items}</ul></div><div class="card"><div class="k">安全底线</div><ul><li>默认关闭；<code>lan_peers.enabled=false</code> 不开任何 UDP/TCP socket。</li><li>只做 queen mail；不开放远程会话、colony 或 worker。</li><li>私有 LAN、单跳；WAN/公网不走此路线。</li><li>组密钥只做准入，节点 ed25519 做身份；密钥不入 URL、日志或 announce。</li><li>announce 只是候选和准入证明，不自动 attach 会话。</li><li>只有 <code>queen_mail.queens</code> 里的本地 queen 可被写信。</li></ul></div></div></section>

<section id="acceptance"><h2><span class="num">07</span>验收链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:28%">测试</th><th style="width:65%">通过条件</th></tr></thead><tbody>{acc_rows}</tbody></table></div>
<h3>Review Focus</h3>
<div class="tablewrap"><table><thead><tr><th style="width:42%">未覆盖输入 / 失败模式</th><th style="width:43%">期望</th><th style="width:15%">Owner</th></tr></thead><tbody>{review_rows}</tbody></table></div>
<div class="callout bad"><b>设计边界：</b>status 仍是 <code>design</code>。没有真实 two-process/LAN E2E、idempotency 和最终全量 CI 前，不把本页当作实现或发布证据。</div></section>

<section id="tasks"><h2><span class="num">08</span>实施任务</h2>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:10%">依赖</th><th style="width:44%">写入范围</th><th style="width:40%">完成检查</th></tr></thead><tbody>{task_rows}</tbody></table></div>
<div class="callout info"><b>先决条件：</b>范围已收敛为 LAN 发现 + 联邦 queen mail；实现前确认 group key 轮换、key-change quarantine、附件 path-only 和 inbound 计数策略。本设计不建议进入 1.0 parity，保持 post-1.0 optional。</div></section>

<section id="risks"><h2><span class="num">09</span>风险、待拍板与来源</h2>
<div class="tablewrap"><table><thead><tr><th style="width:28%">风险</th><th style="width:27%">影响</th><th style="width:45%">缓解</th></tr></thead><tbody>{risk_rows}</tbody></table></div>
<h3>待拍板</h3><ul>{lis(data['open_questions'])}</ul>
<h3>来源</h3><ul>{source_items}</ul></section>

<footer>lan-pipes design · 需求—设计—验收链 · 由 <code>go_hive/scripts/gen_lan_pipes_design.py</code> 生成；<code>--check</code> 防漂移 · 设计状态不冒充实现状态</footer>
</div></body></html>
"""


def validate(data: dict) -> None:
    req_ids = {r["id"] for r in data["requirements"]}
    design_ids = set(data["design"])
    acc_ids = {a["id"] for a in data["acceptance"]}
    task_ids = {t["id"] for t in data["tasks"]}
    for req in data["requirements"]:
        for trace in req["traces"]:
            if trace not in design_ids | acc_ids:
                raise ValueError(f"{req['id']} references unknown {trace}")
    referenced = {trace for req in data["requirements"] for trace in req["traces"]}
    for design in design_ids:
        if design not in referenced:
            raise ValueError(f"{design} is not referenced by a requirement")
    for task in data["tasks"]:
        for dep in task["depends_on"]:
            if dep not in task_ids:
                raise ValueError(f"{task['id']} depends on unknown {dep}")
    if req_ids != {f"R{i}" for i in range(1, len(req_ids) + 1)}:
        raise ValueError("requirements are not contiguous")
    if acc_ids != {f"A{i}" for i in range(1, len(acc_ids) + 1)}:
        raise ValueError("acceptance ids are not contiguous")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    json_text = json.dumps(DATA, ensure_ascii=False, indent=2) + "\n"
    html_text = render_html(DATA)
    validate(DATA)
    if args.check:
        drift = []
        for path, expected in ((OUT_JSON, json_text), (OUT_HTML, html_text)):
            if not path.exists() or path.read_text(encoding="utf-8") != expected:
                drift.append(str(path))
        if drift:
            print("lan-pipes-design: DRIFT: " + ", ".join(drift))
            return 1
        print(
            f"lan-pipes-design: OK ({len(DATA['requirements'])} requirements, "
            f"{len(DATA['acceptance'])} acceptance rows)"
        )
        return 0
    OUT_JSON.write_text(json_text, encoding="utf-8")
    OUT_HTML.write_text(html_text, encoding="utf-8")
    print(f"lan-pipes-design: wrote {OUT_HTML} and {OUT_JSON}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
