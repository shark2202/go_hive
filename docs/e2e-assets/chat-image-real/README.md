# Chat images E2E — real model reads a user-attached screenshot

Lane: `chat-images`. Scope: `POST /api/sessions/{id}/chat` now accepts
`images` / `display_message` and delivers the attachment to the model, parity
with `core/framework/server/routes_execution.py::handle_chat` →
`prepare_queen_input` → `deliver_to_queen`.

## Setup

- Model: `openai/deepseek-flash-1017` at `https://token.tokenbroad.com/v1`
  (vision verified in this run).
- `HIVE_HOME`: a fresh temp directory containing a copy of the user's
  `configuration.json` (the `judge` section removed). `BROADTOKEN_API_KEY` was
  taken from `~/.hive-go/env` and only passed as an environment variable.
- Server: `hive serve -p 8789 --host 127.0.0.1`.

## Flow

1. `POST /api/sessions` `{"queen_name":"queen_technology"}` →
   `01-create-session.response.json`.
2. `POST /api/sessions/{id}/upload-attachment` (multipart `file=screenshot.png`,
   copied from `docs/e2e-assets/vision-caption/fixture/screenshot.png`) →
   `02-upload-attachment.response.json` (`path: data/attachments/screenshot.png`).
3. `POST /api/sessions/{id}/chat` with
   `images:[{type:"image_url",image_url:{url:"hive-attachment://data/attachments/screenshot.png"}}]`
   and a `display_message`, asking for the header, title, amount and buttons →
   `03-chat-hive-ref.*.json`.
4. Poll `conversations/parts/*.json` for the queen's reply →
   `04-transcript-hive-ref.parts.json`.
5. Repeat with the same image inlined as a `data:image/png;base64,...` URI →
   `06-chat-data-uri.*.json`, `07-transcript-data-uri.parts.json`.

## Observed replies

- hive-attachment variant:
  `(1) Header text: Acme Admin / (2) Page title: Create invoice /
  (3) Amount due: 1,280.00 USD / (4) Button labels: Search customers
  (placeholder), Find, Cancel, Submit`
- data: URI variant: `Customer: Northwind Traders · Page title: Create invoice`

Both user turns persisted one `image_content` block, and the saved data-URI
copy is byte-identical to the fixture (sha256 in `summary.json`), mode `0600`.

## Evidence index

| File | Contents |
| --- | --- |
| `01-create-session.*.json` | session create request/response |
| `02-upload-attachment.response.json` | uploaded file name/path |
| `03-chat-hive-ref.*.json` | chat request/response with `hive-attachment://` |
| `04-transcript-hive-ref.parts.json` | transcript through the first reply |
| `05-events-history-hive-ref.json` | `/events/history` after the first reply |
| `06-chat-data-uri.*.json` | chat request/response with `data:` URI |
| `07-transcript-data-uri.parts.json` | transcript through the second reply |
| `08-events-history-data-uri.json` | `/events/history` after the second reply |
| `09-events-jsonl-client-input.txt` | `events.jsonl` client_input_received lines (`image_count`, `image_paths`) |
| `10-serve-log-excerpt.txt` | serve log access lines and startup excerpt |
| `summary.json` | machine-checked assertions (all pass) |

## 未覆盖边界（登记，2026-11-22）

- colony-bound 会话存在 live overseer 时，图片只进入会话转录（`recordRelayedClientTurn`），relay 通道无图片 seam——overseer 看不到图。
- 响应 `status` 仅 `queen`：Go 无 revive 分支，未返回上游的 `queen_revived`。
- webp 不做尺寸探测（stdlib 无解码器），块照常投递。
- `data:` URI base64 不复刻 Python 侧 `validate=False` 的空白容忍。
- 大 PDF 页数统计需整文件读入内存（上游流式）；仅影响 >10MB 引用 PDF 的 `[Attachments]` 页数标注。
- 内联附件沿用上游时间戳命名（sanitize/同名消歧仍只在 upload 端点）。
