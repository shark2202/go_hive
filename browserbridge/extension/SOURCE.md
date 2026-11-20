# Extension source provenance

This directory is a verbatim copy of the Hive Browser Bridge Chrome extension
that ships with the Python GCU browser tools. It is a first-class artifact of
this repository, not a documentation reference.

| Field | Value |
|---|---|
| Upstream path | `/Users/mac/hive/tools/browser-extension/` |
| Extension version | 1.7.4 (`manifest.json`) |
| Extension protocol version | 6 (`EXTENSION_PROTOCOL_VERSION` in `background.js`) |
| Copied on | 2026-11-20 |
| Upstream license | Apache-2.0 (repository root `LICENSE`, copied here as `LICENSE`) |

## What was copied

All upstream files, byte for byte, including the non-runtime `mockups/`
directory:

```
manifest.json
background.js
offscreen.html
offscreen.js
popup.html
popup.js
sidepanel.html
sidepanel.js
wordlist.js
README.md
icons/icon-16.png
icons/icon-32.png
icons/icon-48.png
icons/icon-128.png
mockups/hive.svg
mockups/sidepanel-v2.html
```

Two files are local additions: this `SOURCE.md` and `LICENSE` (the upstream
directory has no license file of its own; the repository root Apache-2.0
license is included so redistributed zips carry terms).

No extension logic, manifest field, permission, or version string was modified.

## Verifying this copy

From the repository root:

```bash
diff -ru tools/browser-extension go_hive/browserbridge/extension \
  --exclude SOURCE.md --exclude LICENSE
```

No output means the runtime files are identical.

## Syncing a newer upstream version

1. Diff first and read the changes:
   ```bash
   diff -ru tools/browser-extension go_hive/browserbridge/extension \
     --exclude SOURCE.md --exclude LICENSE
   ```
2. Refresh the copy, preserving the local provenance and license files:
   ```bash
   rsync -a --delete \
     --exclude SOURCE.md --exclude LICENSE \
     tools/browser-extension/ go_hive/browserbridge/extension/
   ```
3. Update the version/protocol/date table above.
4. Run `go test ./...`; `extension_test.go` validates the manifest and every
   file it references, and `scripts/package_extension.sh --check` validates the
   distributable zip shape.
5. If the extension protocol version changed, update `PARITY.md` and the Go
   bridge's protocol assumptions before claiming compatibility.

## Packaging

`scripts/package_extension.sh` produces
`dist/hive-browser-bridge-<version>.zip` with `manifest.json` at the zip root.
`scripts/package_extension.sh --check` verifies an existing archive.
