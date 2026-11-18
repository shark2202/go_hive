package maintenance

// Parity: framework/maintenance/archive.py

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// Parity: framework/maintenance/archive.py::ArchiveDisposer
type ArchiveDisposer struct {
	inner    DeleteDisposer
	hiveHome string
	runDir   string
	seq      int
	Members  int
}

var archiveOneFunc = func(d *ArchiveDisposer, path string) error {
	return d.archiveOne(path)
}

func NewArchiveDisposer(home hivehome.Home, archiveDir string) (*ArchiveDisposer, error) {
	root := archiveDir
	if root == "" {
		root = filepath.Join(home.Root, "archive")
	}
	root = expandUser(root)
	stamp := time.Now().Format("20060102_150405")
	runDir := filepath.Join(root, "janitor-"+stamp)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	return &ArchiveDisposer{
		inner:    DeleteDisposer{Home: home},
		hiveHome: resolveLongestExisting(home.Root),
		runDir:   runDir,
	}, nil
}

func (d *ArchiveDisposer) ArchivePath() string { return d.runDir }

func (d *ArchiveDisposer) DryRun() bool { return false }

func (d *ArchiveDisposer) Archives() bool { return true }

func (d *ArchiveDisposer) DisposeFile(path string) (int64, error) {
	if err := AssertSafeTarget(d.inner.Home, path); err != nil {
		return 0, err
	}
	if err := archiveOneFunc(d, path); err != nil {
		return 0, err
	}
	return d.inner.DisposeFile(path)
}

func (d *ArchiveDisposer) DisposeDir(path string) (int, int64, error) {
	if err := AssertSafeTarget(d.inner.Home, path); err != nil {
		return 0, 0, err
	}
	if err := archiveOneFunc(d, path); err != nil {
		return 0, 0, err
	}
	return d.inner.DisposeDir(path)
}

func (d *ArchiveDisposer) archiveOne(path string) error {
	resolved := resolveLongestExisting(path)
	arcname, err := filepath.Rel(d.hiveHome, resolved)
	if err != nil || arcname == ".." || strings.HasPrefix(arcname, ".."+string(filepath.Separator)) {
		return fmt.Errorf("archive failed for %s: %v", path, err)
	}
	d.seq++
	final := filepath.Join(d.runDir, fmt.Sprintf("%05d-%s.tar.gz", d.seq, filepath.Base(path)))
	tmp := final + ".tmp"
	if err := writeTarGz(tmp, resolved, filepath.ToSlash(arcname)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("archive failed for %s: %w", path, err)
	}
	if f, openErr := os.OpenFile(tmp, os.O_RDWR, 0); openErr == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("archive failed for %s: %w", path, err)
	}
	d.Members++
	return nil
}

// Parity: framework/maintenance/archive.py::ArchiveDisposer.close
func (d *ArchiveDisposer) Close() string {
	if d.Members == 0 {
		if err := os.Remove(d.runDir); err != nil {
			return d.runDir
		}
		return ""
	}
	return d.runDir
}

func writeTarGz(target, root, arcname string) (err error) {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	defer func() {
		if cerr := tw.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if cerr := gz.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	walkErr := filepath.WalkDir(root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		name := arcname
		if p != root {
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return relErr
			}
			name = filepath.ToSlash(filepath.Join(arcname, rel))
		}
		name = filepath.ToSlash(name)
		if info.IsDir() && !strings.HasSuffix(name, "/") {
			name += "/"
		}
		hdr, hdrErr := tar.FileInfoHeader(info, "")
		if hdrErr != nil {
			return hdrErr
		}
		hdr.Name = name
		if info.Mode()&os.ModeSymlink != 0 {
			link, linkErr := os.Readlink(p)
			if linkErr != nil {
				return linkErr
			}
			hdr.Linkname = link
		}
		if hdrErr := tw.WriteHeader(hdr); hdrErr != nil {
			return hdrErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		src, openErr := os.Open(p)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(tw, src)
		closeErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	return walkErr
}
