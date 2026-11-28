# Third-Party Notices

This file records the third-party components used by this repository, the
license each one is distributed under, and where its source and license text
can be found. It records license names and links only; it does not reproduce
license texts.

Checked on 2026-11-19 against `go.mod` / `go.sum` and
`core/frontend/package.json` / `core/frontend/package-lock.json`. Section 1 is
regenerated from the module graph by `go_hive/scripts/dep_inventory.py`; run
`make deps-check` in `go_hive/go-hive` to verify, `make deps-update` to refresh.

## 1. Go module dependencies (`go_hive/go-hive`)

Source of truth: `go list -m -json all` plus each module's own `LICENSE` /
`COPYING` / `NOTICE` file in the Go module cache (never inferred from the
module path). The generated block below is authoritative; the counts it prints
replace the older hand-maintained numbers.

Role legend:

- **Direct** - required by the `require` block of `go.mod` and imported by the
  Go sources.
- **Indirect** - listed in the second `require` block of `go.mod` as an
  indirect dependency.
- **Build list only** - present in the module build list but not written in
  `go.mod`; typically reached through the module graph, code generation or
  test dependencies. Presence here does not by itself mean the code is linked
  into a released binary.

<!-- BEGIN GENERATED:third-party-notices -->

<!-- 本文件由 `go_hive/scripts/dep_inventory.py` 生成，请勿手工编辑。 -->

Go module graph as generated from `go.mod` / `go.sum` under `go version go1.26.11 darwin/arm64`: 39 external modules, 19 in the compile closure.

| Module | Version | Licence | Role | Source |
| --- | --- | --- | --- | --- |
| `github.com/dustin/go-humanize` | `v1.0.1` | MIT | indirect | [github.com/dustin/go-humanize](https://pkg.go.dev/github.com/dustin/go-humanize@v1.0.1) |
| `github.com/ebitengine/purego` | `v0.11.3` | Apache-2.0 | direct | [github.com/ebitengine/purego](https://pkg.go.dev/github.com/ebitengine/purego@v0.11.3) |
| `github.com/go-rod/rod` | `v0.116.2` | MIT | direct | [github.com/go-rod/rod](https://pkg.go.dev/github.com/go-rod/rod@v0.116.2) |
| `github.com/go-rod/stealth` | `v0.4.9` | MIT | direct | [github.com/go-rod/stealth](https://pkg.go.dev/github.com/go-rod/stealth@v0.4.9) |
| `github.com/google/pprof` | `v0.0.0-20260802141513-ef3492d7dac3` | Apache-2.0 | build list only | [github.com/google/pprof](https://pkg.go.dev/github.com/google/pprof@v0.0.0-20260802141513-ef3492d7dac3) |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | indirect | [github.com/google/uuid](https://pkg.go.dev/github.com/google/uuid@v1.6.0) |
| `github.com/hashicorp/golang-lru/v2` | `v2.0.7` | MPL-2.0 | build list only | [github.com/hashicorp/golang-lru/v2](https://pkg.go.dev/github.com/hashicorp/golang-lru/v2@v2.0.7) |
| `github.com/kr/pretty` | `v0.1.0` | UNVERIFIED | build list only | [github.com/kr/pretty](https://pkg.go.dev/github.com/kr/pretty@v0.1.0) |
| `github.com/mattn/go-isatty` | `v0.0.24` | MIT | direct | [github.com/mattn/go-isatty](https://pkg.go.dev/github.com/mattn/go-isatty@v0.0.24) |
| `github.com/ncruces/go-strftime` | `v1.0.0` | MIT | indirect | [github.com/ncruces/go-strftime](https://pkg.go.dev/github.com/ncruces/go-strftime@v1.0.0) |
| `github.com/odvcencio/gotreesitter` | `v0.57.0` | MIT | direct | [github.com/odvcencio/gotreesitter](https://pkg.go.dev/github.com/odvcencio/gotreesitter@v0.57.0) |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause | indirect | [github.com/remyoudompheng/bigfft](https://pkg.go.dev/github.com/remyoudompheng/bigfft@v0.0.0-20230129092748-24d4a6f8daec) |
| `github.com/ysmood/fetchup` | `v0.2.3` | MIT | indirect | [github.com/ysmood/fetchup](https://pkg.go.dev/github.com/ysmood/fetchup@v0.2.3) |
| `github.com/ysmood/goob` | `v0.4.0` | MIT | indirect | [github.com/ysmood/goob](https://pkg.go.dev/github.com/ysmood/goob@v0.4.0) |
| `github.com/ysmood/gop` | `v0.2.0` | UNVERIFIED | build list only | [github.com/ysmood/gop](https://pkg.go.dev/github.com/ysmood/gop@v0.2.0) |
| `github.com/ysmood/got` | `v0.40.0` | MIT | indirect | [github.com/ysmood/got](https://pkg.go.dev/github.com/ysmood/got@v0.40.0) |
| `github.com/ysmood/gotrace` | `v0.6.0` | UNVERIFIED | build list only | [github.com/ysmood/gotrace](https://pkg.go.dev/github.com/ysmood/gotrace@v0.6.0) |
| `github.com/ysmood/gson` | `v0.7.3` | MIT | indirect | [github.com/ysmood/gson](https://pkg.go.dev/github.com/ysmood/gson@v0.7.3) |
| `github.com/ysmood/leakless` | `v0.9.0` | MIT | indirect | [github.com/ysmood/leakless](https://pkg.go.dev/github.com/ysmood/leakless@v0.9.0) |
| `golang.org/x/mod` | `v0.41.0` | BSD-3-Clause | build list only | [golang.org/x/mod](https://pkg.go.dev/golang.org/x/mod@v0.41.0) |
| `golang.org/x/sync` | `v0.23.0` | BSD-3-Clause | build list only | [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync@v0.23.0) |
| `golang.org/x/sys` | `v0.48.0` | BSD-3-Clause | indirect | [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys@v0.48.0) |
| `golang.org/x/tools` | `v0.50.0` | BSD-3-Clause | build list only | [golang.org/x/tools](https://pkg.go.dev/golang.org/x/tools@v0.50.0) |
| `gopkg.in/check.v1` | `v1.0.0-20180628173108-788fd7840127` | UNVERIFIED | build list only | [gopkg.in/check.v1](https://pkg.go.dev/gopkg.in/check.v1@v1.0.0-20180628173108-788fd7840127) |
| `gopkg.in/yaml.v3` | `v3.0.1` | MIT AND Apache-2.0 | direct | [gopkg.in/yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3@v3.0.1) |
| `modernc.org/cc/v4` | `v4.29.7` | BSD-3-Clause | build list only | [modernc.org/cc/v4](https://pkg.go.dev/modernc.org/cc/v4@v4.29.7) |
| `modernc.org/ccgo/v4` | `v4.36.1` | BSD-3-Clause | build list only | [modernc.org/ccgo/v4](https://pkg.go.dev/modernc.org/ccgo/v4@v4.36.1) |
| `modernc.org/fileutil` | `v1.4.0` | BSD-3-Clause | build list only | [modernc.org/fileutil](https://pkg.go.dev/modernc.org/fileutil@v1.4.0) |
| `modernc.org/gc/v2` | `v2.6.5` | BSD-3-Clause | build list only | [modernc.org/gc/v2](https://pkg.go.dev/modernc.org/gc/v2@v2.6.5) |
| `modernc.org/gc/v3` | `v3.1.5` | BSD-3-Clause | build list only | [modernc.org/gc/v3](https://pkg.go.dev/modernc.org/gc/v3@v3.1.5) |
| `modernc.org/goabi0` | `v0.2.0` | BSD-3-Clause | build list only | [modernc.org/goabi0](https://pkg.go.dev/modernc.org/goabi0@v0.2.0) |
| `modernc.org/libc` | `v1.77.1` | BSD-3-Clause | indirect | [modernc.org/libc](https://pkg.go.dev/modernc.org/libc@v1.77.1) |
| `modernc.org/mathutil` | `v1.7.1` | BSD-3-Clause | indirect | [modernc.org/mathutil](https://pkg.go.dev/modernc.org/mathutil@v1.7.1) |
| `modernc.org/memory` | `v1.12.1` | BSD-3-Clause | indirect | [modernc.org/memory](https://pkg.go.dev/modernc.org/memory@v1.12.1) |
| `modernc.org/opt` | `v0.2.0` | BSD-3-Clause | build list only | [modernc.org/opt](https://pkg.go.dev/modernc.org/opt@v0.2.0) |
| `modernc.org/sortutil` | `v1.2.1` | BSD-3-Clause | build list only | [modernc.org/sortutil](https://pkg.go.dev/modernc.org/sortutil@v1.2.1) |
| `modernc.org/sqlite` | `v1.60.1` | BSD-3-Clause | direct | [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite@v1.60.1) |
| `modernc.org/strutil` | `v1.2.1` | BSD-3-Clause | build list only | [modernc.org/strutil](https://pkg.go.dev/modernc.org/strutil@v1.2.1) |
| `modernc.org/token` | `v1.1.0` | BSD-3-Clause | build list only | [modernc.org/token](https://pkg.go.dev/modernc.org/token@v1.1.0) |

Licence count: Apache-2.0 x2, BSD-3-Clause x20, MIT x11, MIT AND Apache-2.0 x1, MPL-2.0 x1, UNVERIFIED x4 (39 modules total).

Licence identifiers were read from each module's own `LICENSE` / `COPYING` / `NOTICE` file in the Go module cache. `UNVERIFIED` means the module was not present in the cache used to generate this table; it must be checked before a release that links it.

<!-- END GENERATED:third-party-notices -->

### Notes on embedded third-party code

- **gopkg.in/yaml.v3** is dual-licensed by file: files ported from libyaml are
  MIT (Copyright 2006-2011 Kirill Simonov), and the remaining files are
  Apache-2.0 (Copyright 2011-2019 Canonical Ltd). Its upstream `NOTICE`
  carries Copyright 2011-2016 Canonical Ltd.
- **modernc.org/sqlite** is a BSD-3-Clause Go driver. The same module also
  ships the transpiled SQLite amalgamation (public domain, see
  `LICENSE-SQLITE`), the sqlite-vec extension (MIT, see `LICENSE-SQLITE_VEC`)
  and a flattened inventory in `LICENSE-3RD-PARTY.md`. That inventory should
  travel with any redistribution of a binary that links this driver.
- **modernc.org/libc** is BSD-3-Clause and contains code derived from musl
  libc (MIT); its `LICENSE-3RD-PARTY.md` lists the inherited notices.
- **github.com/hashicorp/golang-lru/v2** is MPL-2.0, a file-level copyleft
  license. The module is consumed unmodified from the Go module cache and is
  not vendored into this repository.

## 2. Frontend dependencies (`core/frontend`)

Source of truth: `core/frontend/package.json` for the declared dependency set,
`core/frontend/package-lock.json` (lockfileVersion 3, 509 entries) for the
resolved versions, and npm registry metadata (`npm view <package>@<version>
license repository.url`) for the license identifiers, checked 2026-11-19.

The table lists all 27 direct dependencies: 13 runtime and 14 development.
The 481 transitive packages in the lockfile are not enumerated individually.
No license text is reproduced here; follow the source links for the full text.

| Package | Declared | Resolved | Scope | License | Source |
| --- | --- | --- | --- | --- | --- |
| @fontsource-variable/geist | ^5.3.0 | 5.3.0 | runtime | OFL-1.1 | https://github.com/fontsource/font-files |
| @fontsource-variable/geist-mono | ^5.3.0 | 5.3.0 | runtime | OFL-1.1 | https://github.com/fontsource/font-files |
| clsx | ^2.1.1 | 2.1.1 | runtime | MIT | https://github.com/lukeed/clsx |
| echarts | ^5.6.0 | 5.6.0 | runtime | Apache-2.0 | https://github.com/apache/echarts |
| lucide-react | ^0.575.0 | 0.575.0 | runtime | ISC | https://github.com/lucide-icons/lucide |
| mermaid | ^11.14.0 | 11.14.0 | runtime | MIT | https://github.com/mermaid-js/mermaid |
| react | ^18.3.1 | 18.3.1 | runtime | MIT | https://github.com/facebook/react |
| react-dom | ^18.3.1 | 18.3.1 | runtime | MIT | https://github.com/facebook/react |
| react-markdown | ^10.1.0 | 10.1.0 | runtime | MIT | https://github.com/remarkjs/react-markdown |
| react-router-dom | ^7.1.0 | 7.13.0 | runtime | MIT | https://github.com/remix-run/react-router |
| remark-gfm | ^4.0.1 | 4.0.1 | runtime | MIT | https://github.com/remarkjs/remark-gfm |
| shiki | ^4.5.0 | 4.5.0 | runtime | MIT | https://github.com/shikijs/shiki |
| tailwind-merge | ^3.5.0 | 3.5.0 | runtime | MIT | https://github.com/dcastil/tailwind-merge |
| @tailwindcss/vite | ^4.0.0 | 4.2.0 | development | MIT | https://github.com/tailwindlabs/tailwindcss |
| @testing-library/dom | ^10.4.1 | 10.4.1 | development | MIT | https://github.com/testing-library/dom-testing-library |
| @testing-library/jest-dom | ^6.9.1 | 6.9.1 | development | MIT | https://github.com/testing-library/jest-dom |
| @testing-library/react | ^16.3.2 | 16.3.2 | development | MIT | https://github.com/testing-library/react-testing-library |
| @testing-library/user-event | ^14.6.1 | 14.6.3 | development | MIT | https://github.com/testing-library/user-event |
| @types/node | ^25.3.0 | 25.3.0 | development | MIT | https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/node |
| @types/react | ^18.3.18 | 18.3.28 | development | MIT | https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/react |
| @types/react-dom | ^18.3.5 | 18.3.7 | development | MIT | https://github.com/DefinitelyTyped/DefinitelyTyped/tree/master/types/react-dom |
| @vitejs/plugin-react | ^4.3.4 | 4.7.0 | development | MIT | https://github.com/vitejs/vite-plugin-react |
| jsdom | ^25.0.1 | 25.0.1 | development | MIT | https://github.com/jsdom/jsdom |
| tailwindcss | ^4.0.0 | 4.2.0 | development | MIT | https://github.com/tailwindlabs/tailwindcss |
| typescript | ~5.6.2 | 5.6.3 | development | Apache-2.0 | https://github.com/microsoft/TypeScript |
| vite | ^6.0.0 | 6.4.1 | development | MIT | https://github.com/vitejs/vite |
| vitest | ^4.0.18 | 4.0.18 | development | MIT | https://github.com/vitest-dev/vitest |

License count: Apache-2.0 x2, ISC x1, MIT x22, OFL-1.1 x2 (27 packages total).

Note: the two `@fontsource-variable` packages declare OFL-1.1 because the font
files they distribute are under the SIL Open Font License 1.1. That license
permits bundling and redistribution of the fonts; keep the OFL notice with the
font files.

## 3. Embedded chart render bundles (`go_hive/go-hive/internal/charts/assets`)

`chart_render` embeds two minified browser bundles with `go:embed` so a render
never needs the network. They are byte-identical copies of the Python tool's
static assets (`tools/src/chart_tools/static/`); see
`internal/charts/assets/README.md` for checksums.

| Bundle | Version (self-reported) | License | Source |
| --- | --- | --- | --- |
| `echarts.min.js` | Apache ECharts 5.6.0 | Apache-2.0 | https://github.com/apache/echarts |
| `mermaid.min.js` | Mermaid 11.4.0 | MIT | https://github.com/mermaid-js/mermaid |

- `echarts.min.js` retains its upstream Apache-2.0 banner, including the
  bundled Microsoft TypeScript runtime notice. Apache-2.0 requires keeping the
  LICENSE/NOTICE of the distribution with any redistribution that contains it.
- `mermaid.min.js` retains the MIT notices of the packages bundled into it.

## 4. Redistribution checklist

- Apache-2.0 components (this repository, purego, pprof, echarts, TypeScript,
  and the Apache-2.0 files of yaml.v3) require retaining the LICENSE and any
  NOTICE file; do not remove or rewrite the upstream copyright notices.
- The embedded chart bundles keep the mit/apache headers of their upstream
  builds; do not reformat or strip those banners.
- MPL-2.0 (golang-lru/v2) is file-level copyleft. Do not modify those files
  without publishing the modified source, or replace the dependency.
- OFL-1.1 fonts must keep their license notice when redistributed.
- No GPL, AGPL or LGPL component appears in either dependency set as declared
  by the module/package metadata.
- License texts are available from each source link above and, for Go modules,
  from the `LICENSE*` files in the module cache.

## 5. Verification performed

```text
$ cd go_hive/go-hive && go list -m all | wc -l
40                     # 1 main module + 39 external modules listed above
$ make deps-check      # regenerates section 1 and compares; exit 0 = current
$ shasum -a 256 LICENSE go_hive/go-hive/LICENSE
2cda9484e54c0e82b0f5ea2b5ba372f5875ed260234718c94e70b622d59748de  LICENSE
2cda9484e54c0e82b0f5ea2b5ba372f5875ed260234718c94e70b622d59748de  go_hive/go-hive/LICENSE
$ cd core/frontend && node -e "console.log(Object.keys(require('./package.json').dependencies).length + Object.keys(require('./package.json').devDependencies).length)"
27
```

Re-check procedure:

1. `cd go_hive/go-hive && go list -m all` - every module must appear in the
   table in section 1 with the same version.
2. `go mod download -json <module>@<version>` - read the `Dir` field and open
   the `LICENSE*` files there to confirm the license name.
3. `cd core/frontend && npm view <package>@<resolved-version> license
   repository.url` - compare with the table in section 2.
