# Orca 浏览器 UI E2E — hive 1.0.25-go.1

- 日期：2026-11-20 08:44–08:55 CST
- 环境：Orca 1.4.261（Electron），内置浏览器 page `62932f0c-1f6c-40dd-a759-f9ecf30c5a12`
- 目标：`http://127.0.0.1:8788/`，本机 `hive-embed serve`，真实 LLM（deepseek-flash-1017）
- 方式：`orca tab create` / `snapshot` / `click` / `fill` / `exec read` / `eval` / `console`

## 结果汇总

| # | 用例 | 结果 | 证据 |
| --- | --- | --- | --- |
| 1 | 首页加载与版本号 | PASS | 页面文本含 `v1.0.25-go.1`；`OpenHive // 蜂后网络`、蜂群/蜂后列表渲染 |
| 2 | 中英切换 + 持久化 | PASS | 点 `EN` 后 heading 变英文；`localStorage.hive.locale=en`、`<html lang>=en`；点 `中文` 恢复 `zh-CN` |
| 3 | 凭据页 | PASS | `#/credentials` 显示 `0 active · 314 available`，Built-in 分组渲染 |
| 4 | ask_user 冷加载重现 | PASS | 从空白页打开 `#/queen/queen_growth`，问题「今天先做哪一项？」与 `1.A` / `2.B` 仍在 |
| 5 | ask_user 回答闭环 | PASS | 选 `1.A` 后 Submit 解禁；提交后表单消失，记录 `A`，模型继续回复（见下） |
| 6 | 新建会话真实对话 | PASS | 输入「用一句话回答：你现在是哪个版本？」→ 回复 `go_hive 的 hive-embed，版本 hive 1.0.25-go.1`，可见工具调用 |
| 7 | 控制台错误 | PASS | `orca console --limit 50`：50 条，**0 error** |

## 关键实测文本

用例 5 回答后模型回复：

```
A 收到。但有个问题得先说清楚：A 和 B 从来没有被定义过。
…如果这是 ask_user 往返通路的测试：通了，问题送达、回答回传、标签原样返回，都正常。
```

用例 6 回复：

```
go_hive 的 hive-embed，版本 hive 1.0.25-go.1。
```

## 发现的问题（UX，非回归）

1. **首次进入被 onboarding 弹层挡住**：`Pick a playbook — STEP 1 OF 4` 的黑色 backdrop (`div.absolute.inset-0.bg-black/55`) 拦截所有点击，必须先点 `Begin` 关闭；自动化需先处理该弹层，否则 `click` 返回 `browser_error: element covered`。
2. **点击偶发失败**：`EN` 与 `Send` 各出现一次 `ok:false`，重试即成功（元素位置/动画时序），建议 E2E 里对关键点击做一次重试。
3. `GET /api/sessions/{id}` 不返回 pending/park 字段，前端冷加载重现依赖 `events.jsonl` 回放；本次实测成立，但接口层面无直接断言点。

## 截图

- 会话回复与工具调用：`docs/e2e-assets/orca-ui-chat-1.0.26.png`（Orca 内置浏览器，2026-11-20 08:54）

## 复现命令（节选）

```bash
orca tab create --url http://127.0.0.1:8788/ --json
orca snapshot --page <pageId> --interactive --compact --json
orca click --page <pageId> --element @eNN --json
orca fill --page <pageId> --element @eNN --value "..." --json
orca exec --page <pageId> --command "read" --json
orca console --page <pageId> --limit 50 --json
```
