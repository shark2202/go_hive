# prompt-catalog 对照证据（首轮 system prompt / skill catalog / turn_context）

本目录是 `go_hive/docs/prompt-parity.md` 的机器可读证据，由两个 opt-in 探针生成（仅本地 stub LLM，不触外网）：

```bash
# 渲染 golden：SkillsManager（queen 配置）+ 相位过滤 + description cap + reminder/turn-context 正文
uv run --offline python go_hive/scripts/prompt_catalog_parity.py

# live 双跑：同一 fixture + 同一 configuration.json，Python `hive serve` vs Go `hive serve`
uv run --offline python go_hive/scripts/prompt_system_parity.py

# 系统提示词分段归因（读上面 live 抓包）
uv run --offline python go_hive/scripts/prompt_system_sections.py \
    --python go_hive/docs/e2e-assets/prompt-catalog/system-prompt.python.txt \
    --go go_hive/docs/e2e-assets/prompt-catalog/system-prompt.go.txt \
    --out go_hive/docs/e2e-assets/prompt-catalog
```

固定 fixture：`go_hive/go-hive/internal/skills/testdata/prompt-catalog/fixture.json`
（`small`/`edge` 两个磁盘 profile + `caps`/`caps_compact` 合成表；含 NBSP 截断、300/304 字符边界、
XML 转义、YAML colon 修复、visibility 相位、跨 scope 重名、symlink 位置）。

归一化：`<HIVE_HOME>` / `<HOME>` / `<BUNDLED>` / `<REPO>` / `<SESSION_ID>` / `<TIMESTAMP>` / `<PORT>`；
所有语义文本逐字保留。

## 文件

| 文件 | 内容 |
| --- | --- |
| `summary.txt`, `diff.json` | 渲染 golden 10 例逐字结论（当前 10/10 identical） |
| `python/`, `go/` | 每个 golden case 两侧的 canonical 文本 |
| `python-raw/` | Python 侧未归一化的原始渲染（排查发现流程用；canonical 版本见 `python/`） |
| `live-summary.txt`, `live-diff.json` | live 双跑结论与元数据（system prompt / 首轮 messages / catalog / manifest / turn_context） |
| `system-prompt.{python,go}.txt` | 首轮 system prompt 全文（canonical） |
| `system-prompt.diff` | 系统提示词 unified diff |
| `system-prompt.sections.{json,txt}` | 分段归因：公共前缀 / Python 独有段 / Go 独有段 / CJK 计数 |
| `first-turn-messages.{python,go}.txt` | 开场轮 messages（system 之外；含 `<system-reminder>` 注入块） |
| `reminder-catalog.{python,go}.txt` | 注入块中的 skills catalog 段（当前逐字一致） |
| `reminder-searchable.{python,go}.txt` | 注入块中的 searchable-tools manifest 段（当前逐字一致，307 chars） |
| `user-turn-messages.{python,go}.txt` | 第一条用户消息轮：turn-context 注入位置证据 |
| `turn-context-live.{python,go}.txt` | live turn_context 正文（Python 有，Go 无：无编译期注册的 package） |
| `turn-context-live.diff` | 上者的 unified diff |

## 当前结论

- 渲染 golden：**10/10 identical**（catalog small/edge ×3 相位、caps full/compact、reminder wrapper、turn-context 正文）。
- live skills catalog 段：**逐字一致**（3,985 chars / 20 skills，independent 相位）。
- live system prompt：**整段逐字一致**（21,584 / 21,584 chars，`system-prompt.diff` 0 字节）——
  identity+soul 2,576 + queen 基础提示词 18,820 + credentials 184（+2 个 `\n\n` 分隔符）。
  Python 独有的 19,004 chars 已移植；Go 独有的 59,618 chars
  （`browser-automation` 48,944 + `terminal-tools-foundations` 10,674 整篇被 tool-gating 预激注入）
  已从 queen 路径移除，`browser-automation` / `terminal-tools-foundations` 回到 catalog 按需读取。
  两侧均无 CJK 字符。详见 `prompt-parity.md` §3。
- live searchable manifest：**逐字一致（307 / 307 chars）**——两侧都播报 per-queen allowlist − eager 的 2 个
  工具（`chart_render`、`web_scrape`）。Go 的 lazy 层仍可加载 51 个 flat 工具，但播报集按上游规则收窄；
  `search_tools` 的 select/keywords/inventory 与「schema 未加载」守卫不变。见 `prompt-parity.md` §4 / §4.1。
- 首轮 opening messages：**逐字一致（4,344 / 4,344 chars，`first-turn-messages.diff` 0 字节）**——
  非 system 内容只有那一个 `<system-reminder>`（manifest + catalog）。用户轮残余只剩 Python-only 的
  turn_context 注入（45 chars，P-05）。
- turn_context：配置键 `turn_context.<name>.enabled` 两侧一致（1 键）；注入正文/包装逐字一致（45 chars）；
  live 注入面 Python=entry point 包、Go=编译期 registry（本次 Go 未链接任何 package，故 live 不注入），
  见 `prompt-parity.md` §5。