# go_hive 试用版 · QUICKSTART（2026-11-22）

## 安装

```bash
# 从归档安装（六平台之一）
tar xzf hive-<platform>.tar.gz && cd hive-<platform>
./install.sh          # 默认装到 ~/.hive-go，可 HIVE_HOME=... 覆盖
```

或直接使用 slot 产物：`go_hive/dist/slots/<最新 slot>/`（每 2 小时一版）。

## 首次启动

```bash
~/.hive-go/start.sh              # serve on 127.0.0.1:8788
curl -s localhost:8788/api/health # -> 200
```

浏览器打开 `http://127.0.0.1:8788`。设置模型槽（LLM key 放 `~/.hive-go/env`，不要提交到任何仓库）。

## 从 Python 版 Hive 迁移

- 同一 `HIVE_HOME` 直接可用：启动时自动跑 v1/v2/v3 迁移（含备份）。
- 回滚：迁移保留原文件；停止 go 版后用 Python 版原样启动即可。
- exports 基准：`$HIVE_HOME`（D2 决策 A）。

## 体验重点（语义一致面）

1. **chat**：queen 对话、任务提醒、记忆 notes（1.0.15 族）。
2. **colony**：创建/触发 colony，worker 报告与升级，park/resume 跨重启。
3. **playbook**：Python `.play.py`（需系统 python3）与 Go 原生 `.play.json` 两种。
4. **memory**：`/api/memories`、recall（15k 轮级索引在本机 <0.4s 冷建）。
5. **CLI**：`hive mcp …` 14 动词、`hive colony …`、`hive trigger …` 等与上游 help/退出码对齐。
6. **崩溃恢复**：kill -9 后重启，会话继续、工具结果与 park 均在。

## 已知边界

见同目录 `KNOWN-GAPS.md`（外部未验证 5 类、设计性登记差异 9 条、功能缺口清单）。
