import { readFileSync } from "node:fs";
import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The dashboard ships inside Hive, so it shows Hive's version: the one in
// core/pyproject.toml (aden-hive), not this package.json's, which is never released.
// This tree is also vendored at go_hive/go-hive/frontend, where core/pyproject.toml
// sits three levels up; a standalone go_hive checkout has neither, so the last
// resort is this package's own version.
const HIVE_VERSION = (() => {
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
