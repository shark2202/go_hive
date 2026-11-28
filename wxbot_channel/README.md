# wxbot-channel

WeChat iLink bot channel for the Hive Go channel host. It is an independent
Python project: the Go `hive` process starts it as a stdio plugin, and the
plugin bridges WeChat messages to a queen session through the
`hive-channel/1` JSON-line protocol.

```
WeChat iLink  ⇄  wechatbot-sdk  ⇄  wxbot-channel  ⇄ stdio JSON-RPC ⇄  hive serve
```

## Requirements

- Python >= 3.9
- A WeChat account that can log in through the iLink bot QR flow
- `hive` built with the stdio channel host (`internal/channels`)
- `wechatbot-sdk>=0.3.0` (installed by `uv sync` / `pip install -e .`)

## Install

```bash
cd go_hive/wxbot_channel
uv sync
```

For development without a WeChat login:

```bash
uv run python -m wxbot_channel --version
uv run python -m unittest discover -s tests -v
```

## CLI

The standalone operator CLI uses the same credential file as the channel. Run
these commands before starting `hive serve`; they print diagnostics to stderr
and only metadata (never the token) to stdout.

### `login`

```bash
# Interactive QR login; PNG + HTML + URL are written next to the credential.
uv run python -m wxbot_channel login \
  --cred-path ~/.hive-go/channels/wxbot/credentials.json

# Open the generated HTML page automatically (the PNG is embedded).
uv run python -m wxbot_channel login \
  --cred-path ~/.hive-go/channels/wxbot/credentials.json --open

# Put the PNG somewhere else; HTML/URL use the same stem/directory.
uv run python -m wxbot_channel login \
  --cred-path ~/.hive-go/channels/wxbot/credentials.json \
  --qr-output /tmp/wxbot-login.png

# Machine-readable metadata after login (no token in the output).
uv run python -m wxbot_channel login \
  --cred-path ~/.hive-go/channels/wxbot/credentials.json --json

# Ignore an existing credential file and force a new QR flow.
uv run python -m wxbot_channel login --force --timeout 300
```

Parameters:

| Flag | Default | Meaning |
|---|---|---|
| `--cred-path` | `$WXBOT_CRED_PATH` or `~/.wechatbot/credentials.json` | Credential JSON to create/replace; must match the channel's `cred_path`. |
| `--base-url` | `https://ilinkai.weixin.qq.com` | iLink API base URL. |
| `--force` | off | Re-login even when credentials already exist. |
| `--qr-output` | `<cred-dir>/login-qr.png` | QR PNG path; HTML and URL files use the same stem/directory. |
| `--qr-url-output` | `<cred-dir>/login-qr.txt` | Plain URL file (0600). |
| `--open` | off | Open the generated HTML page after the QR is written. |
| `--timeout` | `0` | Overall login timeout in seconds; `0` uses the SDK's QR refresh limit. |
| `--json` | off | Print `{cred_path,user_id,account_id,base_url,saved_at}`; never the token. |
| `--debug` | off | Verbose stderr logs. |

### `logout`

```bash
uv run python -m wxbot_channel logout \
  --cred-path ~/.hive-go/channels/wxbot/credentials.json --json
```

Parameters:

| Flag | Default | Meaning |
|---|---|---|
| `--cred-path` | same as `login` | Credential file to remove. |
| `--json` | off | Print `{cred_path,removed}`. |
| `--debug` | off | Verbose stderr logs. |

`logout` only removes the local credential file; it does not call a remote
logout endpoint. The next `login` asks for a QR scan again.

### `serve` / default

```bash
uv run python -m wxbot_channel serve --debug
# equivalent for the Hive host: no arguments
uv run python -m wxbot_channel
```

## Hive configuration

Add one channel section to `$HIVE_HOME/configuration.json`. `queen_id` is the
only required plugin key; point it at a queen profile that exists in the same
Hive home.

```json
{
  "channels": {
    "wxbot": {
      "enabled": true,
      "command": [
        "/Users/mac/hive/go_hive/wxbot_channel/.venv/bin/python",
        "-m",
        "wxbot_channel"
      ],
      "cwd": "/Users/mac/hive/go_hive/wxbot_channel",
      "queen_id": "queen_growth",
      "title_prefix": "WeChat",
      "allow_users": [],
      "max_images": 8,
      "max_text_chars": 4000
    }
  }
}
```

`command`/`args`/`cwd`/`env` are the Go host's stdio-plugin extensions; they
are not upstream Python entry points. The host passes this whole section to the
plugin in `hello.config`, plus a private `state_dir` under
`$HIVE_HOME/channels/wxbot`.

Restart `hive serve` after editing the section. `GET /api/channels` should show
the channel in `running` state. If it is `failed`, check the `hive serve` log;
the plugin keeps stdout clean and writes all diagnostics to stderr.

### Configuration keys

| Key | Default | Meaning |
|---|---|---|
| `queen_id` | required | Queen profile id to open a session with. `queen` is accepted as an alias. |
| `title_prefix` | `WeChat` | Dashboard title prefix; the WeChat conversation id is appended. |
| `allow_users` | all | Optional string or list of WeChat `user_id`s allowed to talk to the queen. |
| `shared_session` | `false` | If true, every WeChat conversation shares one Hive session. |
| `session_id` | none | Optional existing Hive session to resume for every new conversation. |
| `max_images` | `8` | Incoming images forwarded per message (host hard limit is 8). |
| `max_text_chars` | `4000` | Outgoing text is split into chunks at this size. |
| `cred_path` | `<state_dir>/credentials.json` | wechatbot-sdk credential file. Keep it private. |
| `base_url` | SDK default (`ilinkai.weixin.qq.com`) | iLink API base. |
| `adapter` | `wechatbot-sdk` | Only the real adapter is built in; tests inject a fake. |

If `cred_path` is omitted from the channel section, the plugin defaults to
`<state_dir>/credentials.json`. That is **not** the same as the CLI's default
(`~/.wechatbot/credentials.json`). Always pass the same `--cred-path` to
`login`/`logout`, or set `cred_path` explicitly as in the example above.

Do not put the credential JSON in this repository. The default location is
inside Hive's private `state_dir`, which is already outside source control.

## Login and first message

On first start the SDK prints the QR URL to **stderr** and writes three
artifacts under `<state_dir>/`:

- `login-qr.png` — scannable QR image
- `login-qr.html` — a page that displays the image
- `login-qr.txt` — the raw URL

Open the PNG/HTML and scan it with WeChat. After login, send a private message
to the bot; the bridge opens a queen session and forwards the message. The
dashboard shows the session with the title `WeChat · <wechat-user-id>`.

The QR callback writes the image to disk because the Hive channel protocol has
no "show a QR code" frame. The serve log names the PNG/HTML paths; open either
file to scan. The files are short-lived login capabilities and are written
with mode 0600.

## Message mapping

| WeChat input | Hive input |
|---|---|
| text | `send.text` |
| image(s) | base64 in `send.images` (max 8, each <= 4 MiB) |
| file | downloaded into `<state_dir>/inbox/` and referenced as `[attachment: <path>]` |
| voice transcript | `[voice] <transcript>` if the SDK supplied one |
| quoted message | `[quoted] <text>` |
| video | a textual notice; previews are not forwarded |

Hive output:

| Hive output | WeChat |
|---|---|
| `message` | text reply, split at `max_text_chars` |
| `question` | a numbered question/options message |
| `error` | `Hive error: ...` |
| `files` | image/file/video upload via the SDK |
| `idle` | stop the typing indicator |

Workers and playbooks still run inside Hive; this channel only exposes the
queen conversation.

## Protocol

The plugin implements the host contract in
[`../../../go_hive/contracts/channel-plugin-protocol.json`](../../contracts/channel-plugin-protocol.json)
(the normative copy is in the Go repository) and the Go host implementation in
`internal/channels`:

- stdout: one JSON object per line, JSON-RPC 2.0 shaped; no logs
- stderr: free-form logs, forwarded by the host
- handshake: `hello` → `welcome(plugin_version, capabilities)` → `start`
- plugin→host: `open_session`, `send`, `subscribe`, `unsubscribe`, `set_title`, `judge`
- host→plugin: `output` notifications, ordered per subscription with `seq`
- shutdown: host `stop`, then stdin close and process teardown

The bridge handles `message`, `question`, `error`, `files` and `idle` output
kinds. It deliberately does not invent any new wire methods.

## Security and limits

- Only `allow_users` (when configured) may reach the queen.
- Incoming files are saved under the channel state directory, never in the
  repository.
- The plugin never logs the `hello.config` object, which may contain the
  credential path or other operator settings.
- Images larger than 4 MiB are skipped with a textual notice; the host also
  enforces the hard limit.
- This channel runs on the same machine as `hive serve` because outgoing
  `files` are absolute paths read by the plugin and incoming attachments are
  local paths handed to the queen.

## Verification

Offline (no WeChat account required):

```bash
uv run python -m unittest discover -s tests -v
uv run python -c 'import wxbot_channel, wxbot_channel.bridge; print("import ok")'
```

Real end-to-end requires a scanned WeChat login:

1. configure the section above and restart `hive serve`;
2. open `<state_dir>/login-qr.png` (or `login-qr.html`) and scan it;
3. send a WeChat message and confirm the queen reply arrives in WeChat;
4. check `GET /api/channels` reports `running` and the dashboard shows the
   same session title.

The repository's unit tests use a fake adapter and a fake host transport; they
prove protocol/session mapping but are not evidence that a real WeChat account
logged in. Record the real QR/login and message exchange separately when
claiming production readiness.
