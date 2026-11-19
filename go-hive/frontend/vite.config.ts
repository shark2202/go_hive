import { readFileSync } from "node:fs";
import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The dashboard ships inside Hive, so it shows Hive's version. When the tree is
// the Go port (go_hive/go-hive/frontend), read internal/version/version.go so the
// sidebar matches `hive --version`; otherwise fall back to the upstream
// core/pyproject.toml version, then this package's own version.
// HIVE_APP_VERSION overrides everything for scripted builds.
const HIVE_VERSION = (() => {
  const override = process.env.HIVE_APP_VERSION?.trim();
  if (override) return override;
  try {
    const goVersion = readFileSync(path.resolve(__dirname, "../internal/version/version.go"), "utf8");
    const match = goVersion.match(/const\s+Version\s*=\s*"([^"]+)"/);
    if (match) return match[1];
  } catch {
    // Not the Go tree; keep looking.
  }
  const candidates = [
    path.resolve(__dirname, "../pyproject.toml"), // core/frontend layout
    path.resolve(__dirname, "../../../core/pyproject.toml"), // vendored layout
  ];
  for (const file of candidates) {
    let pyproject: string;
    try {
      pyproject = readFileSync(file, "utf8");
    } catch {
      continue;
    }
    const start = pyproject.indexOf("[project]");
    if (start < 0) continue;
    const table = pyproject.slice(start).split(/\n\[/)[0]; // up to the next table
    const match = table.match(/^version\s*=\s*"([^"]+)"/m);
    if (match) return match[1];
  }
  const pkg = JSON.parse(readFileSync(path.resolve(__dirname, "package.json"), "utf8"));
  return pkg.version as string;
})();

export default defineConfig({
  plugins: [react(), tailwindcss()],
  define: {
    __APP_VERSION__: JSON.stringify(HIVE_VERSION),
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    proxy: {
      "/api": {
        target: "http://localhost:8787",
        changeOrigin: true,
      },
    },
  },
});
