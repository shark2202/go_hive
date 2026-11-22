# Third-Party Notices

This file records the third-party components used by this repository, the
license each one is distributed under, and where its source and license text
can be found. It records license names and links only; it does not reproduce
license texts.

Checked on 2026-11-19 against `go.mod` / `go.sum` and
`core/frontend/package.json` / `core/frontend/package-lock.json`.

## 1. Go module dependencies (`go_hive/go-hive`)

Source of truth: `go list -m all` run in `go_hive/go-hive` with Go 1.26.5.
That command returns 29 modules: the main module `github.com/shark2202/go_hive/go-hive`
plus the 28 external modules below. License identifiers were read from each
module's own `LICENSE` file in the Go module cache after `go mod download`,
not inferred from the module path.

Role legend:

- **Direct** - required by the `require` block of `go.mod` and imported by the
  Go sources.
- **Indirect** - listed in the second `require` block of `go.mod` as an
  indirect dependency.
- **Build list only** - present in the module build list but not written in
  `go.mod`; typically reached through the module graph, code generation or
  test dependencies. Presence here does not by itself mean the code is linked
  into a released binary.

| Module | Version | License | Role | Source |
| --- | --- | --- | --- | --- |
| github.com/dustin/go-humanize | v1.0.1 | MIT | Indirect | https://github.com/dustin/go-humanize |
| github.com/ebitengine/purego | v0.11.3 | Apache-2.0 | Direct | https://github.com/ebitengine/purego |
| github.com/google/pprof | v0.0.0-20260802141513-ef3492d7dac3 | Apache-2.0 | Build list only | https://github.com/google/pprof |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause | Indirect | https://github.com/google/uuid |
| github.com/hashicorp/golang-lru/v2 | v2.0.7 | MPL-2.0 | Build list only | https://github.com/hashicorp/golang-lru |
| github.com/mattn/go-isatty | v0.0.24 | MIT | Direct | https://github.com/mattn/go-isatty |
| github.com/ncruces/go-strftime | v1.0.0 | MIT | Indirect | https://github.com/ncruces/go-strftime |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause | Indirect | https://github.com/remyoudompheng/bigfft |
| golang.org/x/mod | v0.41.0 | BSD-3-Clause | Build list only | https://pkg.go.dev/golang.org/x/mod |
| golang.org/x/sync | v0.23.0 | BSD-3-Clause | Build list only | https://pkg.go.dev/golang.org/x/sync |
| golang.org/x/sys | v0.48.0 | BSD-3-Clause | Indirect | https://pkg.go.dev/golang.org/x/sys |
| golang.org/x/tools | v0.50.0 | BSD-3-Clause | Build list only | https://pkg.go.dev/golang.org/x/tools |
| gopkg.in/check.v1 | v0.0.0-20161208181325-20d25e280405 | BSD-2-Clause | Build list only | https://github.com/go-check/check |
| gopkg.in/yaml.v3 | v3.0.1 | MIT AND Apache-2.0 (file-level split) | Direct | https://github.com/go-yaml/yaml/tree/v3 |
| modernc.org/cc/v4 | v4.29.7 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/cc |
| modernc.org/ccgo/v4 | v4.36.1 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/ccgo |
| modernc.org/fileutil | v1.4.0 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/fileutil |
| modernc.org/gc/v2 | v2.6.5 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/gc |
| modernc.org/gc/v3 | v3.1.5 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/gc |
| modernc.org/goabi0 | v0.2.0 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/goabi0 |
| modernc.org/libc | v1.77.1 | BSD-3-Clause | Indirect | https://gitlab.com/cznic/libc |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause | Indirect | https://gitlab.com/cznic/mathutil |
| modernc.org/memory | v1.12.1 | BSD-3-Clause | Indirect | https://gitlab.com/cznic/memory |
| modernc.org/opt | v0.2.0 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/opt |
| modernc.org/sortutil | v1.2.1 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/sortutil |
| modernc.org/sqlite | v1.60.1 | BSD-3-Clause | Direct | https://gitlab.com/cznic/sqlite |
| modernc.org/strutil | v1.2.1 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/strutil |
| modernc.org/token | v1.1.0 | BSD-3-Clause | Build list only | https://gitlab.com/cznic/token |

License count: Apache-2.0 x2, BSD-2-Clause x1, BSD-3-Clause x20, MIT x3,
MPL-2.0 x1, MIT AND Apache-2.0 x1 (28 modules total).

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
29                     # 1 main module + 28 external modules listed above
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
