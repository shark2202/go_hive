# QC 结果检验

| 检验 | 结果 | 证据 |
| --- | --- | --- |
| C2 parity 引用 | PASS | annotation_exit=0；4551 个测试；refs 0 missing |
| C4 六平台产物 | PASS | 8 个产物；version=hive 1.0.24-go.1 |
| C4b UI 可分发性 | PASS | 磁盘 dist=有；embed 脚本=有；hive-embed=有 |
| C5 无 cgo | PASS | import "C" 文件 0 个 |
| C6 文档口径 | PASS | 残留 811 口径：无 |
| C7 测试回归 | PASS | 当前 4551，台账峰值 4160 |
| C1 make ci | PASS | wiring_check: OK —— 3 个接线文件、12 项要求全部命中（root=/Users/mac/hive/go_hive/go-hive） |
| C3 运行时冒烟 | PASS | SMOKE: PASS (60605) |

**8/8 PASS**

