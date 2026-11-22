# i18n 覆盖静态盘点报告（lane `i18n-coverage`）

- 生成：2026-11-23；基线 `main` @ `62e92f3b`（worktree `.wt/i18n-coverage`，branch `i18n-coverage`）
- A = `go_hive/go-hive/frontend`（Go vendored，含手写 i18n）；B = `core/frontend`（上游 Python 版前端）
- 边界：**不改任何产品代码**（frontend src / Go / contracts / Makefile 均未动）、不跑前端构建、不 push；仅新增
  `go_hive/scripts/i18n_coverage.py` + `go_hive/docs/e2e-assets/i18n-coverage/**`（含本报告）。
- 结论一句话：A 字典 1238 键、明确引用 1148 键（其中 **8 条字典缺失 → zh-CN 回落英文，属真实待补**）；
  未引用字典 98 条（强 dead 候选仅 1 条）；B 无 i18n 层（A-only 1238 / B-only 0 / 值差 0）；A/B 62 条
  content_diff 均为既有 i18n 归因，本盘点不新增差异断言。

---

## 1. i18n 机制与提取规则

### 1.1 A 树机制（通读 `src/i18n/index.tsx` 后确认）

- **英文源文案即 key**：`en` 直接渲染 key；`zh-CN` 查 merged 字典，缺条目时回退 key 本身
  （`index.tsx` L61–69 的 `translate()`：`hasOwnProperty(zh, key) ? zh[key] : undefined`，空则 `|| key`）。
  因此「缺失翻译」在 zh-CN 下表现为**中文界面里出现英文文案**，不会报错。
- 字典 4 文件按 `index.tsx` 的 spread 顺序合并：`chat → pages → settings → core`（后 spread 覆盖前，
  注释与实现一致）。
- 调用形态：`const { t } = useI18n()` 后 `t("…")`；全树无 `i18n.t(`、无 `<Trans>` 组件；
  `LocaleSwitcher.tsx` 是 `src/i18n/` 下唯一的 UI 组件（`t("Language")`），必须计入产品代码。
- 测试用 `translate("zh-CN", …)` 与 `result.current.t(…)`，只出现在 `*.test.tsx`（本盘点单列不计入头条）。

### 1.2 提取规则（脚本 `rules` 字段同步落盘）

1. 字典解析：TS tokenizer 而非行正则——支持裸标识符 key（`queen:`）、prettier 折行 value、转义引号
   （`\"work\"`）、行内注释。
2. 引用范围：`src/**/*.{ts,tsx}` 排除 `src/i18n/zh/**`、`src/i18n/index.tsx`（运行时本体）、
   `*.test.*`/`*.spec.*`、`src/test/**`；且**文件必须经 `useI18n()` 取 `t`**（排除如 `DebugPanel.tsx`
   里 `const t = (c?: number) => …` 的数值格式化局部函数），局部 `t` 遮蔽（`const t =`、`for (const t of …)`、
   `function t`）按作用域排除。
3. 静态 key：第一参为**唯一字符串字面量**（或无可插值反引号）。
4. 表达式参数：三元等表达式里 **depth-0** 字面量计入；**排除**比较操作数（`s.mode === "review"` 的 `"review"`）、
   顶层 `+` 拼接片段、嵌套对象/数组/调用内的数据字面量。
5. Tokenizer 处理了三个已知陷阱（本轮实测踩过并修掉）：
   - 模板插值 `${…}` 内部**复位为 token**（含对象字面量花括号配平）——`Sidebar.tsx:106`
     `` `${queen.name} — ${t("running {tool}", …)}` `` 这类调用原先会被整段吞进模板；
   - JSX `</tag>` 不被误读为正则字面量；`${s.replace(/"/g, '""')}` 内的正则按上下文跳过；
   - JSX 散文里的引号（`Don't`、`it's`）不当作字符串。
   - **自检**：产品文件 raw `t(` 文本计数 **1539 = token 识别 1539，0 个文件有差**（`tokenizer_self_check`）。
6. 动态 `t(expr)`：先按「同名 const 字面量 / 索引查表（`ROLE_TITLE[role]`）/ 属性名池（`t(row.title)`，同文件 +
   直接 import 的本地模块）/ 本地 helper 函数内字面量」解析为**候选键**（29 个调用、110 个候选）；解析不到的
   21 处逐条落盘。候选键**只**用于 dead-key 降级与「低置信缺失」清单，**不进头条缺失数字**。
7. dead key 分层（互斥）：字典 − 明确引用 = **98**；再切三层——
   `仅字典出现`（源码里无任何同文字面量）/ `文本另有字面量`（别处存在同文字面量，可能被数据表/动态 t() 命中）/
   `动态候选命中`（仅由第 6 步候选覆盖）。
8. 值体检：`value == key`、key/value 占位符集合不一致、空值。

### 1.3 B 树判定（先验证再比较）

用证据确认 B **没有 i18n 层**，而非假设：无 `src/i18n/` 目录、189 个产品文件中 0 个含
`useI18n`/locale/i18n import 标记、字典 0 键；原始 `t(` 仅 3 处且全部是 `DebugPanel.tsx:710-712` 的
数值格式化局部函数（非 i18n）。因此 c)/d) 两问为结构结论（0 不是"缺失"，而是"不存在该机制"）。

---

## 2. 命令与产物（可复现）

```bash
git -C /Users/mac/hive worktree add /Users/mac/hive/.wt/i18n-coverage -b i18n-coverage 62e92f3b
cd /Users/mac/hive/.wt/i18n-coverage
uv run --no-project python go_hive/scripts/i18n_coverage.py        # JSON + md 生成（约 60s）
bash go_hive/scripts/merge_postcheck.sh                            # 回归确认（无 Go 改动）
```

产物：

- `go_hive/scripts/i18n_coverage.py` — 盘点脚本（纯标准库；`--root/--tree-a/--tree-b/--out/--prior` 可调）
- `go_hive/docs/e2e-assets/i18n-coverage/i18n-coverage.json` — 全量机器可读清单（约 320KB，含全部 key/调用点
  /候选/未解析/校准）
- `go_hive/docs/e2e-assets/i18n-coverage/i18n-coverage.md` — 脚本生成的汇总表
- 本报告 `REPORT.md`

脚本 stdout（实测）：

```
tree A: dictionary 1238 keys (1290 entries, 47 cross-file duplicates)
tree A: t() references: 1148 distinct (1482 static sites, 8 expression sites); dynamic 29 resolved / 21 unresolved
tree A: missing translations 8; statically unreferenced dictionary entries 98 (dict-only 1, literal-elsewhere 45, dynamic-candidate 52)
tree B: i18n dir=False marker files=0 raw t( sites=3 dictionary keys=0
tokenizer self-check: raw t( 1539 vs token-detected 1539 (0 files with gap)
diff: A-only 1238 / B-only 0 / value-changed 0
```

---

## 3. 原始数字

| 指标 | 数值 |
| --- | ---: |
| A 字典条目（去重前，chat 194 / pages 207 / settings 836 / core 53） | 1290 |
| A 字典唯一键（合并后，跨文件重复 47 键；文件内重复 0） | 1238 |
| A 被 `t()` 静态字面量引用的唯一键 | 1140 |
| A 静态调用点（含重复） | 1482 |
| A 表达式参数里的显式字面量键（三元，来自 4 个调用） | 8 |
| A 明确引用键合计（静态 ∪ 表达式字面量） | 1148 |
| A 动态 `t(expr)`：已解析调用 / 未解析调用 | 29 / 21 |
| **A 缺失翻译（明确引用但字典无）** | **8** |
| A 静态未引用字典条目（三层互斥） | 98 = 1 + 45 + 52 |
| A 翻译值 == 英文键 | 15 |
| A key/value 占位符集合不一致 / 空值 | 0 / 0 |
| B 字典键 / 被引用键 / 缺失（同口径） | 0 / 0 / 0 |
| A/B：A 有 B 无 / B 有 A 无 / 值不同 | 1238 / 0 / 0 |
| 测试文件口径（信息性）：44 文件 / 2 个静态调用点 / 1 个键 | — |

---

## 4. 四类结果

### 4.1 (a) A 缺失 key = **8 条**（真实待补翻译）

全部来自「三元分支字面量」调用；按文件：`QueenSoulSection.tsx` 6 条、`SentinelConnectorsSection.tsx` 2 条。

| 文件:行 | 缺失 key | 场景 |
| --- | --- | --- |
| `QueenSoulSection.tsx:159` | `Her changes wait for your approval` | soul 模式开关（review） |
| `QueenSoulSection.tsx:159` | `Her changes apply at once` | 同上（即时应用） |
| `QueenSoulSection.tsx:203` | `Her first draft` | 提案标签（bootstrap） |
| `QueenSoulSection.tsx:203` | `Her reflection` | 提案标签（反射） |
| `QueenSoulSection.tsx:271` | `Try her with these changes ({count} question)` | 校验按钮（单数） |
| `QueenSoulSection.tsx:271` | `Try her with these changes ({count} questions)` | 校验按钮（复数） |
| `SentinelConnectorsSection.tsx:276` | `Connected to {account}.` | 连接状态（slack） |
| `SentinelConnectorsSection.tsx:276` | `Connected as {account}.` | 连接状态（其他渠道） |

验证与归因（实测）：

- 8 条 key 在 `src/i18n/zh/*.ts` 中 `grep -F` 均无条目；按 `translate()` 逻辑 zh-CN 必回落英文。
- 两个组件都是**既有 62 条 content_diff 中的 `i18n-refactor` 文件**（`docs/frontend-surface/frontend-surface-diff.json`
  `content_diff_files`），B 侧存在同源英文（如 `QueenSoulSection.tsx:201`、`SentinelConnectorsSection.tsx` 的
  `Connected{channel === "slack" ? " to " : " as "}`）→ 这 8 条**不是 A/B 内容漂移**，而是 A 字典漏条：
  英文文案在改造成 `t()` 时没有进字典。属**真实待补翻译**（本 lane 不改产品代码，建议后续 lane 补字典）。
- 说明既有盘点的「缺失 0」为何没发现：`frontend_surface_diff.py::T_CALL_RE` 只匹配「整参为字面量」的调用，
  三元参数调用被计入 `t_dynamic_calls`（51）但未解析；本盘点的表达式字面量规则正好覆盖该盲区。

另有 **43 条「动态候选缺失」**（低置信，不进头条）：`home.tsx:525` 26 条（提示词分类名如
`Customer Success & Support` 与蜂后名 `Athena` 等）、`models.tsx` 17 条（模型描述/占位符，如
`Claude 4 family - Fast, balanced, and most capable`）、`ProviderManager/models` 交叉的 `Vision fallback`。
这些字符串经 `t(section.name)`、`t(row.description)` 等动态调用渲染；是否翻译（蜂后名大概率不译）需产品拍板。
完整清单见 JSON `tree_a.missing_dynamic_candidates`。

### 4.2 (b) A 未引用字典条目 = **98 条**

| 层级 | 条数 | 说明 | 例子 |
| --- | ---: | --- | --- |
| 仅字典出现（强 dead-key 候选） | 1 | 源码里无任何同文字面量 | `Delete this memory?`（字典里另有被引用的长版本 `Delete this memory? It can be restored from Removed.`） |
| 文本另有字面量 | 45 | 源码别处存在同文字面量（数据表/单位列表），可能经动态 t() 命中 | `CRM (HubSpot)`、`Charts & diagrams`、`daily`/`hourly`/`hours`/`minutes`/`weekly`/`stopped` |
| 动态候选命中（低置信） | 52 | 仅由动态解析候选覆盖 | `Pick before answering`、`Weekdays`、`removed`/`restored`/`wrote`、`Hidden background` |

结论：**只有 1 条可以列为"无源码足迹"的死键候选**；45 + 52 两条（合计 97）都可能是运行时键
（数据表驱动、后端下发），**不能据此删字典**。与既有冻结盘点校准：旧 `dictionary_but_unused` 91 条中
85 条在当前口径下仍未引用（6 条现已确认被引用，见下）；98 条未引用的其余拆解为
**12 条 1.0.15 新增键 + 1 条 `Delete this memory?`**（在基线字典内、旧盘点判为已引用，当前无任何引用点，
疑为文案改写后的遗留条目——它正是本轮唯一的强 dead 候选）。6 条旧未用键现被引用：
`Language`（LocaleSwitcher，旧脚本整目录跳过了 `src/i18n/`）、`Enter new` / `Paste your`（嵌套调用被旧正则
整体吞掉）、`Failed to remove {target}`、`{date} at {time}`（第二参含模板/花括号，旧正则不匹配）、
以及一条长文案（同上）。这正是本轮 tokenizer 相对旧正则的净增量。

### 4.3 (c) B 同口径 = 0 / 0 / 0

证据见 §1.3：B 无 `src/i18n/`、0 个 i18n 标记文件、0 字典键、原始 `t(` 3 处均为数值格式化。
因此 B 的「被引用 key 总数 / 缺失 key」为 0 是**机制不存在**，不是覆盖率为 0 的质量问题。

### 4.4 (d) A vs B 差异

- **B 有 A 无 = 0；A 有 B 无 = 1238（全部字典键）；值不同 = 0**（B 无 zh 值可比，值差异清单为空）。
- 附加启发式（仅 presence，非语义断言）：1238 个 A 键的英文文案在 B 产品源码中——精确子串命中 1069、
  占位符通配命中 150、**未命中 19**。19 条未命中为 A 侧新增/改写文案（`Scheduler fired`、`Webhook fired`、
  `Hey there, what can I help you with?`、`Claude Code · {n} chat(s)`、`{count} archived task(s)…`、
  `Uploading {count} file(s)…`、`{count} skills found`、`Copied {count} times` 等），属 Go 侧后加功能文案，
  不构成差异断言。完整清单见 JSON `tree_b.counterpart_heuristic.misses`。

---

## 5. 结论与归因（哪些是实测、哪些归谁）

1. **真实待补翻译（实测、确定）**：8 条（§4.1）。zh-CN 下必然显示英文；建议由 i18n lane 补
   `settings.ts` 条目（本 lane 不动产品代码）。
2. **既有 content_diff 归因（引用，不在本 lane 新增）**：A/B 前端源码 1106 共有路径中 62 条内容差异，
   分类 i18n-wrap 13 / i18n-refactor 47 / i18n-plumbing 1 / version-resolution 1（
   `docs/frontend-surface-diff.md §1`）；i18n 层（`src/i18n/**` 7 文件 + 字典键）本身是白名单增量。
   本盘点**未新增任何 A/B 差异断言**（B 无 i18n 是登记过的口径）。
3. **需产品决策（低置信）**：43 条动态候选缺失（§4.1 末段）——大多是数据表文案（提示词分类、模型描述、
   persona 标签、蜂后名）；蜂后名等专有名词可能按不译处理。
4. **不可据此清理**：45（literal-elsewhere）+ 52（dynamic-candidate）条"未引用"条目；仅 `Delete this memory?`
   有较强 dead 证据。
5. **值体检（实测）**：`value == key` 15 条与旧盘点完全一致（`GitHub`/`Notion`/`OAuth` 等品牌技术名，
   故意不译）；占位符集合不一致 0；空值 0。
6. **方法论校准（实测）**：旧正则 `T_CALL_RE` 在当前树回放 = **1134 个引用键（与
   `frontend-surface-diff.md §9` 记载的 1134 完全一致）**；本 tokenizer 口径 = 1140 静态键，
   **仅旧方法 0 条、仅新方法 6 条**（§4.2 末段解释），是严格超集。

---

## 6. 无法静态判定 / 残留不确定项

- **运行时动态键**：21 处 `t(expr)` 无法解析（清单见 JSON `tree_a.unresolved_sites`），其中
  `t(runPhase)`、`t(title)`、`t(label)`、`t(u)`、`t(w)`、`t(kicker/lede)`、`t(author)`、
  `t(richDescription(primary))`、`t(memoryAuthorLabel(…) ?? …)`、`t(group.title)` 等若运行时值与字典键
  相同则命中，否则回落英文——静态无法判定。
- **后端下发字符串**（状态、作者、变更动词等）作为 key 的可能性；dead 分层因此保留三档。
- **JSX 纯文本未包 `t()`** 的硬编码英文不在本口径（只核 `t()` 引用）；如需可另开扫描。
- **动态解析是超集启发**（属性名池/同名常量），候选键可能偏多或偏少，已全程标注低置信。
- **未做真实渲染验证**：按 lane 边界不跑前端构建/浏览器；zh-CN 各路由渲染、语言切换的最终外观未在本
  lane 实测（建议后续 Playwright/Chrome 冒烟，见既有 §7 后续项）。

## 7. 门禁与边界

- `bash go_hive/scripts/merge_postcheck.sh`（worktree 内，提交前跑）**exit 0**：
  `gofmt clean` / `wiring_check: OK（21 文件、68 项）` / C12 `verdict=PASS` / arch `一致（commit 9684ec82eea5）`。
  本 lane 无 Go 改动，属回归确认。
- 改动清单仅 `go_hive/scripts/i18n_coverage.py` + `go_hive/docs/e2e-assets/i18n-coverage/**`；无产品代码、
  无构建、无 push。
- 复现：见 §2 两条命令；JSON 即原始证据（含全部 key、调用点、候选、未解析、校准与自检数字）。
