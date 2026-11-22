package storage

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func NeedsMigrationV2(homeRoot string) bool {
	return !fileExists(filepath.Join(homeRoot, ".migrated-v2"))
}

func RunMigrationV2(homeRoot string, exportsDir string) error {
	if !NeedsMigrationV2(homeRoot) {
		return nil
	}
	h, err := hivehome.ResolveAt(homeRoot)
	if err != nil {
		return err
	}
	if exportsDir == "" {
		exportsDir = "exports"
	}
	migrateV2Colonies(h, exportsDir)
	migrateV2QueenSessions(h)
	migrateV2Memories(h)
	_ = os.RemoveAll(filepath.Join(h.Root, "queen"))

	if err := os.MkdirAll(h.Root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(h.Root, ".migrated-v2"), []byte("1\n"), 0o644)
}

func migrateV2Colonies(h hivehome.Home, exportsDir string) {
	if !fileExists(exportsDir) {
		return
	}
	colonies := h.ColoniesDir()
	if err := os.MkdirAll(colonies, 0o755); err != nil {
		return
	}
	entries := readDirNames(exportsDir)
	for _, name := range entries {
		src := filepath.Join(exportsDir, name)
		info, err := os.Stat(src)
		if err != nil || !info.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		target := filepath.Join(colonies, name)
		if fileExists(target) {
			continue
		}
		_ = copyTree(src, target)
	}
}

func migrateV2QueenSessions(h hivehome.Home) {
	oldSessions := filepath.Join(h.Root, "queen", "session")
	if !fileExists(oldSessions) {
		return
	}
	newSessions := filepath.Join(h.QueensDir(), "default", "sessions")
	if err := os.MkdirAll(newSessions, 0o755); err != nil {
		return
	}
	for _, name := range readDirNames(oldSessions) {
		src := filepath.Join(oldSessions, name)
		info, err := os.Stat(src)
		if err != nil || !info.IsDir() {
			continue
		}
		target := filepath.Join(newSessions, name)
		if fileExists(target) {
			continue
		}
		_ = os.Rename(src, target)
	}
}

func migrateV2Memories(h hivehome.Home) {
	oldGlobal := filepath.Join(h.Root, "queen", "global_memory")
	if !fileExists(oldGlobal) {
		return
	}
	newGlobal := filepath.Join(h.MemoriesDir(), "global")
	if fileExists(newGlobal) {
		mergeMemoryFiles(oldGlobal, newGlobal)
		return
	}
	if err := os.MkdirAll(newGlobal, 0o755); err != nil {
		return
	}
	for _, name := range readDirNames(oldGlobal) {
		src := filepath.Join(oldGlobal, name)
		info, err := os.Stat(src)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		_ = copyFileWithMode(src, filepath.Join(newGlobal, name))
	}
}

func mergeMemoryFiles(srcDir, dstDir string) {
	for _, name := range readDirNames(srcDir) {
		src := filepath.Join(srcDir, name)
		info, err := os.Stat(src)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		target := filepath.Join(dstDir, name)
		if fileExists(target) {
			continue
		}
		_ = copyFileWithMode(src, target)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readDirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func copyFileWithMode(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFileWithMode(src, dst)
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	for _, name := range readDirNames(src) {
		if err := copyTree(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			return err
		}
	}
	return nil
}

func movePath(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}
