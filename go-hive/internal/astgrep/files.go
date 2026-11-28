package astgrep

import (
	"bytes"
	"os"
)

// DefaultMaxFileBytes is the per-file read cap. A larger file is skipped
// without parsing: structural search is for source, not generated blobs.
const DefaultMaxFileBytes int64 = 1 << 20

// readCandidate reads one candidate under the byte cap and rejects binary
// content. ok is false for a deliberate skip (oversize, binary, non-regular),
// while err is a real read failure reported as a parse issue.
func readCandidate(path string, maxBytes int64) (data []byte, ok bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, false, nil
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return nil, false, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, false, nil
	}
	return data, true, nil
}
