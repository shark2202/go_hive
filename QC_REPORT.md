# QC 结果检验

| 检验 | 结果 | 证据 |
| --- | --- | --- |
| C2 parity 引用 | PASS | annotation_exit=0；5608 个测试；refs 0 missing |
| C4 六平台产物 | PASS | 9 个产物；version=hive 1.0.26-go.1 |
| C4b UI 可分发性 | PASS | 磁盘 dist=有；embed 脚本=有；hive-embed=有 |
| C4c 归档含四个二进制 | PASS | 6 个归档；问题=无；本地 darwin/arm64 hive-1.0.26-go.1-darwin-arm64.tar.gz；hive exit=0；hive-cred exit=0；hive-browser exit=0；browserbridge exit=0 |
| C5 无 cgo | PASS | import "C" 文件 0 个 |
| C6 文档口径 | PASS | 残留 811 口径：无 |
| C7 测试回归 | PASS | 当前 5608，台账峰值 4160 |
| C8 表面审计（强制） | PASS | 前端缺失 0（待修 0）；Python 缺失 0（待修 0）；卫生问题 0；Go 多余 11（仅提示）；报告 go_hive/docs/surface-audit.md；strict |
| C9 证据审计锚点 | PASS | integrations-evidence-anchors: 8/8 anchors PASS (5 sites) |

**9/9 PASS**

