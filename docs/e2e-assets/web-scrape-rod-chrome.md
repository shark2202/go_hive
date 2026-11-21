# web_scrape：rod + 系统 Chrome 真实渲染证据

日期：2026-11-21 · lane `web-scrape-chrome-render` · 基线 `98120156`

## 环境

| 项 | 值 |
| --- | --- |
| OS | macOS 26.7.1 (darwin/arm64) |
| Go | go1.26.5 |
| 浏览器 | Google Chrome 158.0.8127.37（`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`，系统安装，非 rod 下载） |
| 渲染库 | `github.com/go-rod/rod v0.116.2`、`github.com/go-rod/stealth v0.4.9`（bundle v2.7.3） |
| 构建 | `CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build ./...` |

## 命令与结果

### 1. 离线构建（依赖已在模块缓存）

```console
$ cd go_hive/go-hive
$ CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build ./...
$ echo $?
0
```

### 2. 单测（默认不启浏览器）

```console
$ CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
    go test ./internal/tools/integrations/ ./internal/charts/
ok  github.com/aden-hive/hive-go/internal/tools/integrations  3.136s
ok  github.com/aden-hive/hive-go/internal/charts              9.191s
```

### 3. 真实 Chrome（opt-in）

```console
$ CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
    HIVE_SCRAPE_CHROME_E2E=1 \
    go test ./internal/tools/integrations/ -run TestBatch3WebScrapeRealChromeRendersJS -v
=== RUN   TestBatch3WebScrapeRealChromeRendersJS
    scrape_render_test.go:252: system browser: /Applications/Google Chrome.app/Contents/MacOS/Google Chrome
    scrape_render_test.go:283: JS off (static tokenizer): body=""
    scrape_render_test.go:293: JS on (rod + Chrome): body="Rendered\n\nClient-side marker: rendered-by-chrome."
--- PASS: TestBatch3WebScrapeRealChromeRendersJS (2.47s)
```

fixture 是一个本地 `httptest` 服务：

- `/js` 返回 `<div id="root"></div>` + 内联脚本，只有在浏览器执行脚本后才出现
  `Client-side marker: rendered-by-chrome.`；
- **JS off**（静态 HTML 直接进现有 tokenizer）正文为空；
- **JS on**（`batch3RenderWithBrowser` → rod → 系统 Chrome）正文非空，证明 JS 已执行。

同一测试在两次渲染后断言共享 browser 仍存活（`browser.Version()`），即 `batch3ChromePool`
复用了同一进程，没有每次调用重启浏览器。

### 4. 进程不泄漏

测试进程退出后：

```console
$ pgrep -fl "Google Chrome" | grep -i "headless\|rod" ; echo $?
1
```

无 headless/rod 标记的 Chrome 进程残留（输出中的日常桌面 Chrome 进程来自本机 GUI 会话，
启动时间早于测试）。rod launcher 默认 `Leakless` 在本 port 中显式保持开启，Go 进程退出时
强制结束浏览器进程组并清理临时 profile。

## 说明：为什么真实渲染证据在 helper 级

`webScrapeAction` 在任何导航前执行 SSRF 校验，回环/私网地址会被明确阻断（Python 同语义），
因此不能把 `127.0.0.1` fixture 直接喂给 action。真实 Chrome 测试验证渲染 helper 与现有
提取器的组合；action 级管线（预检 fetch → renderer → tokenizer → artifact）由
`scrape_render_test.go` 的注入用例覆盖（JS 渲染、无 Chrome、渲染失败、超时）。
