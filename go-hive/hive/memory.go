package hive

import (
	"os"

	"github.com/shark2202/go_hive/go-hive/internal/hiveconf"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
	"github.com/shark2202/go_hive/go-hive/internal/soul"
)

// MemoryFile is one stored memory note: frontmatter plus file metadata.
//
// Parity: framework/agents/queen/queen_memory_v2.py::MemoryFile
type MemoryFile = memory.MemoryFile

// MemoryWriteOptions carries the audit context of one memory write.
//
// Parity: framework/agents/queen/memory_log.py::write
type MemoryWriteOptions = memory.WriteOptions

// GlobalMemoryDir is the cross-queen memory directory.
//
// Parity: framework/agents/queen/queen_memory_v2.py::global_memory_dir
func GlobalMemoryDir(home Home) string {
	if home.Root == "" {
		return ""
	}
	return memory.GlobalMemoryDir(home.Root)
}

// QueenMemoryDir is one queen's memory directory.
//
// Parity: framework/agents/queen/queen_memory_v2.py::queen_memory_dir
func QueenMemoryDir(home Home, queenID string) string {
	if home.Root == "" {
		return ""
	}
	return memory.QueenMemoryDir(home.Root, queenID)
}

// ColonyMemoryDir is one colony's memory directory.
//
// Parity: framework/agents/queen/queen_memory_v2.py::colony_memory_dir
func ColonyMemoryDir(home Home, colonyID string) string {
	if home.Root == "" {
		return ""
	}
	return memory.ColonyMemoryDir(home.Root, colonyID)
}

// ListMemories lists a memory directory's notes, newest first.
//
// Parity: framework/agents/queen/queen_memory_v2.py::scan_memory_files
func ListMemories(dir string) []MemoryFile {
	return memory.ScanMemoryFiles(dir)
}

// ReadMemory reads one memory note by filename. Filenames are validated so a
// caller cannot escape the directory.
//
// Parity: framework/agents/queen/memory_log.py::_read
func ReadMemory(dir, filename string) (string, error) {
	path, err := memory.SafeMemoryPath(filename, dir)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteMemory writes one memory note, appending the memory log entry the
// runtime expects.
//
// Parity: framework/agents/queen/memory_log.py::write
func WriteMemory(dir, filename, content string, opts MemoryWriteOptions) (string, error) {
	return memory.Write(dir, filename, content, opts)
}

// Soul is a queen's accumulated identity: versioned sections of dated items.
//
// Parity: framework/agents/queen/soul.py::Soul
type Soul = soul.Soul

// SoulItem is one dated soul entry.
//
// Parity: framework/agents/queen/soul.py Item
type SoulItem = soul.Item

// SoulConfig is the persisted soul configuration for one queen.
//
// Parity: framework/config.py::SoulConfig
type SoulConfig = soul.SoulConfig

// LoadSoul reads one queen's soul. A missing soul is an empty one.
//
// Parity: framework/agents/queen/soul.py::load
func LoadSoul(home Home, queenID string) *Soul {
	if home.Root == "" {
		return nil
	}
	return soul.Load(home.QueensDir(), queenID)
}

// SoulConfigFor resolves one queen's soul configuration from the Hive home.
//
// Parity: framework/config.py::get_soul_config
func SoulConfigFor(home Home, queenID string) *SoulConfig {
	if home.Root == "" {
		return nil
	}
	return soul.GetSoulConfig(hiveconf.Load(home), queenID)
}
