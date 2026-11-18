# QA 过程审计（v2：按合并提交回溯）

| 合并 | 文件 | 新增测试 | parity 注解 | 共享文件 | 结论 |
| --- | ---: | ---: | ---: | --- | --- |
| merge w4/embed: 可选 go:embed 前端打包 | 9 | 2 | 2 | go_hive/go-hive/Makefile, go_hive/go-hive/internal/server/app.go | VIOLATION：非 integrator 改共享文件 |
| merge w4/pdf | 4 | 29 | 29 | - | OK |
| merge w4/spec | 4 | 16 | 17 | - | OK |
| merge w4/qmail | 12 | 20 | 26 | - | OK |
| merge w4/send | 18 | 59 | 59 | - | OK |
| merge w4/x1 | 2 | 50 | 50 | - | OK |
| merge w4/t2 | 9 | 9 | 16 | - | OK |
| merge w4/t1 | 17 | 37 | 38 | - | OK |
| merge w4/t2: search_messages/search_timeline | 4 | 31 | 24 | - | WARN：注解 24 < 测试 31（以 make ci 为准） |
| merge w4/t1: terminal_* 工具族 | 7 | 16 | 16 | - | OK |
| merge wave3/int: 26 个注册函数装配 + 运行时适配 + 工具注册 | 8 | 6 | 7 | go_hive/go-hive/internal/server/app.go | OK |
| merge wave2/tools2: 集成工具第二批 10 模块 / 19 actio | 17 | 34 | 34 | - | OK |
| merge wave3/parityfix: 修正 36 处 parity 注解与豁免理 | 14 | 0 | 36 | - | OK |
| Merge branch 'main' into wave3/int | 19 | 18 | 25 | - | OK |
| merge wave2/adapt: colony/host 运行时导出与 seam | 8 | 9 | 13 | - | OK |
| merge wave2/gap: compact-and-fork 与 superses | 5 | 9 | 12 | - | OK |
| merge fix/integration: 消除合并后包级符号冲突 | 2 | 0 | 0 | - | OK |
| merge lane/j | 6 | 15 | 16 | - | OK |
| merge lane/i | 19 | 100 | 108 | - | OK |
| merge lane/h | 20 | 31 | 32 | - | OK |
| merge lane/g | 6 | 29 | 32 | - | OK |
| merge lane/f | 8 | 40 | 85 | - | OK |
| merge lane/e | 9 | 83 | 111 | - | OK |
| merge lane/d | 6 | 44 | 66 | - | OK |
| merge lane/b | 10 | 39 | 90 | - | OK |
| merge lane/a | 7 | 44 | 80 | - | OK |
| merge lane/c: credentials+soul+channels 路由 | 6 | 34 | 54 | - | OK |
| Merge pull request #7480 from 23821/yakbarbe | 3 | 0 | 0 | - | OK |
| Merge branch 'main' into yakbarber/anthropic | 129 | 0 | 0 | - | OK |
| Merge pull request #7500 from aden-hive/rele | 7 | 0 | 0 | - | OK |
| Merge pull request #7498 from aden-hive/rele | 16 | 0 | 0 | - | OK |
| Merge pull request #7495 from aden-hive/rele | 17 | 0 | 0 | - | OK |
| Merge pull request #7490 from aden-hive/docs | 1 | 0 | 0 | - | OK |
| Merge pull request #7488 from aden-hive/feat | 15 | 0 | 0 | - | OK |
| Merge origin/main into feature/v1-refactor | 42 | 0 | 0 | - | OK |
| Merge pull request #7487 from aden-hive/chor | 15 | 0 | 0 | - | OK |
| Merge pull request #7486 from aden-hive/docs | 19 | 0 | 0 | - | OK |
| Merge pull request #7484 from aden-hive/docs | 278 | 0 | 0 | - | OK |
| Merge pull request #7356 from iinaa-eimrit/f | 2 | 0 | 0 | - | OK |
| Merge pull request #7198 from sachin180119/d | 3 | 0 | 0 | - | OK |
| Merge branch 'feature/oauth-revamp' | 27 | 0 | 0 | - | OK |
| Merge branch 'feature/merge-to-file-ops' int | 14 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/feature | 42 | 0 | 0 | - | OK |
| Merge branch 'feat/ask-user-chat-display' | 8 | 0 | 0 | - | OK |
| Merge branch 'main' into feature/colony-push | 7 | 0 | 0 | - | OK |
| Merge branch 'feat/tasks-system' | 7 | 0 | 0 | - | OK |
| Merge branch 'feat/api-colonies-import' | 3 | 0 | 0 | - | OK |
| Merge pull request #7143 from aden-hive/fix/ | 4 | 0 | 0 | - | OK |
| Merge pull request #7132 from vincentjiang77 | 6 | 0 | 0 | - | OK |
| merge: pull upstream/main into feat/colony-s | 43 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/feat/ca | 14 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/feat/qu | 16 | 0 | 0 | - | OK |
| Merge branch 'pr-7093-vincent' into feat/col | 14 | 0 | 0 | - | OK |
| Merge branch 'main' into fix/image-coordinat | 6 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/main' i | 81 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/main' i | 33 | 0 | 0 | - | OK |
| Merge pull request #7081 from vincentjiang77 | 21 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/main' i | 22 | 0 | 0 | - | OK |
| Merge branch 'fix/worker-tab-groups' into fe | 7 | 0 | 0 | - | OK |
| Merge branch 'feature/colony-sqlite' into fe | 44 | 0 | 0 | - | OK |
| Merge branch 'feature/full-image-size' | 11 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/fix/bro | 4 | 0 | 0 | - | OK |
| Merge branch 'refactor/worker-message' into  | 7 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/main' i | 370 | 0 | 0 | - | OK |
| Merge branch 'main' into feat/gui-ux-updates | 3 | 0 | 0 | - | OK |
| Merge branch 'main' into feat/gui-ux-updates | 13 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/main' | 0 | 0 | 0 | - | OK |
| Merge branch 'feature/new-colony' | 420 | 0 | 0 | - | OK |
| Merge pull request #7046 from vincentjiang77 | 2 | 0 | 0 | - | OK |
| Merge branch 'feature/colony-creation' into  | 65 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/feat/te | 10 | 0 | 0 | - | OK |
| Merge branch 'feature/new-colony' into featu | 4 | 0 | 0 | - | OK |
| Merge branch 'feature/new-colony' into featu | 3 | 0 | 0 | - | OK |
| Merge branch 'feature/new-colony' into featu | 4 | 0 | 0 | - | OK |
| Merge branch 'feature/hive-experimental-comp | 5 | 0 | 0 | - | OK |
| Merge branch 'aden-hive:main' into main | 13 | 0 | 0 | - | OK |
| Merge branch 'fix/after-colony-refresh' into | 3 | 0 | 0 | - | OK |
| Merge remote-tracking branch 'origin/feature | 4 | 0 | 0 | - | OK |
| Merge branch 'feature/hive-experimental-comp | 69 | 0 | 0 | - | OK |
| Merge branch 'feature/colony-orchestrate' in | 27 | 0 | 0 | - | OK |

## 结论

- 合并 80 个；共享文件越界 1 个。
- 共享文件只允许 integrator 合并改；其余出现即越界。
- 「注解 < 测试」只是线索；硬门禁是 `make ci` 里的 check_parity（逐测试必须注解）。
