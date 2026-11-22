# go get 镜像实测（module 路径交付验证）

- 时间：2026-11-23 00:32 CST
- 镜像：`github.com/shark2202/go_hive` @ `0d6e037`（wave-77 publish，sync @ be40a2c7）
- 环境：`GOPROXY=direct`、`GOSUMDB=off`、`GIT_CONFIG http.version=HTTP/1.1`（规避 GitHub 443 抖动）、go1.26.11、CGO_ENABLED=0
- 命令：
```bash
mkdir gogettest2 && cd gogettest2 && go mod init example.com/gogettest2
for M in github.com/shark2202/go_hive/browserbridge \
         github.com/shark2202/go_hive/go-hive \
         github.com/shark2202/go_hive/go-hive-cloud; do go get "$M@0d6e037"; done
```

## 实测输出（逐字）

```
=== github.com/shark2202/go_hive/browserbridge
go: downloading github.com/shark2202/go_hive v1.0.26-go.1.0.20261122162740-0d6e037edd37
go: added github.com/shark2202/go_hive/browserbridge v0.0.0-20261122162740-0d6e037edd37
OK(1)
=== github.com/shark2202/go_hive/go-hive
go: downloading github.com/shark2202/go_hive/go-hive v0.0.0-20261122162740-0d6e037edd37
go: added github.com/shark2202/go_hive/go-hive v0.0.0-20261122162740-0d6e037edd37
OK(1)
=== github.com/shark2202/go_hive/go-hive-cloud
go: downloading github.com/shark2202/go_hive/go-hive-cloud v0.0.0-20261122162740-0d6e037edd37
go: added github.com/shark2202/go_hive/go-hive-cloud v0.0.0-20261122162740-0d6e037edd37
OK(1)
```

## 结果

| 模块 | 结果 | 解析出的伪版本 |
|---|---|---|
| `github.com/shark2202/go_hive/browserbridge` | ✅ | `v0.0.0-20261122162740-0d6e037edd37` |
| `github.com/shark2202/go_hive/go-hive` | ✅ | 同上 |
| `github.com/shark2202/go_hive/go-hive-cloud` | ✅ | 同上 |

`go.mod` 见同目录 `go.mod.sample`。

## 说明

- 首次尝试（commit `cf2d6de`）中 `browserbridge` 成功，但 `go-hive` 遇到一次 GitHub TLS 抖动（`SSL_ERROR_SYSCALL`）；改用 HTTP/1.1 并重试后在 `0d6e037` 三模块一次通过。
- 仓库根出现的 `github.com/shark2202/go_hive v1.0.26-go.1.0.2026...` 是 Go 为解析子目录模块而拉取的仓库级版本基准，非独立模块。
