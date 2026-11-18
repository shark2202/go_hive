# QA 过程审计

| 分支 | 提交 | 变更文件 | 新增测试 | parity 注解行 | 触碰共享文件 | 结论 |
| --- | ---: | ---: | ---: | ---: | --- | --- |
| `fix/integration` | 0 | 0 | 0 | 0 | - | OK |
| `lane/a` | 0 | 0 | 0 | 0 | - | OK |
| `lane/b` | 0 | 0 | 0 | 0 | - | OK |
| `lane/c` | 0 | 0 | 0 | 0 | - | OK |
| `lane/d` | 0 | 0 | 0 | 0 | - | OK |
| `lane/e` | 0 | 0 | 0 | 0 | - | OK |
| `lane/f` | 0 | 0 | 0 | 0 | - | OK |
| `lane/g` | 0 | 0 | 0 | 0 | - | OK |
| `lane/h` | 0 | 0 | 0 | 0 | - | OK |
| `lane/i` | 0 | 0 | 0 | 0 | - | OK |
| `lane/j` | 0 | 0 | 0 | 0 | - | OK |
| `w4/img` | 0 | 0 | 0 | 0 | - | OK |
| `w4/pdf` | 0 | 0 | 0 | 0 | - | OK |
| `w4/qmail` | 0 | 0 | 0 | 0 | - | OK |
| `w4/send` | 0 | 0 | 0 | 0 | - | OK |
| `w4/spec` | 0 | 0 | 0 | 0 | - | OK |
| `w4/t1` | 0 | 0 | 0 | 0 | - | OK |
| `w4/t2` | 0 | 0 | 0 | 0 | - | OK |
| `w4/x1` | 0 | 0 | 0 | 0 | - | OK |
| `wave2/adapt` | 0 | 0 | 0 | 0 | - | OK |
| `wave2/gap` | 0 | 0 | 0 | 0 | - | OK |
| `wave2/int` | 0 | 0 | 0 | 0 | - | OK |
| `wave2/rel` | 0 | 0 | 0 | 0 | - | OK |
| `wave2/tools2` | 0 | 0 | 0 | 0 | - | OK |
| `wave2/ui` | 1 | 1 | 0 | 0 | - | OK |
| `wave2/ver` | 0 | 0 | 0 | 0 | - | OK |
| `wave3/int` | 0 | 0 | 0 | 0 | - | OK |
| `wave3/parityfix` | 0 | 0 | 0 | 0 | - | OK |

## 结论

- 分支 28 个；共享文件违规 0 个。
- 共享文件（app.go / go.mod / Makefile / registry.go）只允许 integrator 分支改；其余分支出现即视为越界。
- 「parity 注解行 < 新增测试数」只作为线索，最终以 `make ci` 的 parity 门禁为准（它要求逐测试注解）。
