package sessionreport

// Parity: framework/server/_session_report.py (module, no upstream tests)

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	MarkerName        = "user_report.json"
	binaryBudgetBytes = 1024 * 1024 * 1024
)

var maxSingleBinary int64 = 256 * 1024 * 1024

var Severities = []string{"low", "medium", "high", "critical"}

var textSuffixes = map[string]struct{}{
	".json":  {},
	".jsonl": {},
	".md":    {},
	".txt":   {},
	".log":   {},
	".yaml":  {},
	".yml":   {},
	".csv":   {},
}

type credentialPattern struct {
	re   *regexp.Regexp
	repl string
}

var credentialPatterns = []credentialPattern{
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`), "[REDACTED_JWT]"},
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9_\-\.=]+`), "Bearer [REDACTED]"},
	{regexp.MustCompile(`(?i)(api[_-]?key|api[_-]?secret|secret[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|x-api-key|x-auth-token)("?\s*[:=]\s*"?)([A-Za-z0-9_\-\.\+/]{16,})`), "$1$2[REDACTED_SECRET]"},
	{regexp.MustCompile(`https?://[^\s"']+?[?&](?:code|state|access_token|refresh_token|id_token|token|key|secret|password)=[^\s"']*`), "[REDACTED_AUTH_URL]"},
	{regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_\-]{16,}`), "[REDACTED_KEY]"},
	{regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{16,}`), "[REDACTED_KEY]"},
	{regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`), "[REDACTED_KEY]"},
	{regexp.MustCompile(`\bAIza[A-Za-z0-9_\-]{20,}`), "[REDACTED_KEY]"},
}

type BundleStats struct {
	Files             int      `json:"files"`
	TextFiles         int      `json:"text_files"`
	BinaryFiles       int      `json:"binary_files"`
	BinaryOmitted     int      `json:"binary_omitted"`
	ImagesStripped    int      `json:"images_stripped"`
	BytesUncompressed int      `json:"bytes_uncompressed"`
	OmittedPaths      []string `json:"omitted_paths"`
}

type UserReportMarker struct {
	Label       string `json:"label"`
	LabelSource string `json:"label_source"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	ReportedAt  string `json:"reported_at"`
	Reporter    string `json:"reporter"`
}

type manifestStats struct {
	Files          int      `json:"files"`
	TextFiles      int      `json:"text_files"`
	BinaryFiles    int      `json:"binary_files"`
	BinaryOmitted  int      `json:"binary_omitted"`
	ImagesStripped int      `json:"images_stripped"`
	OmittedPaths   []string `json:"omitted_paths"`
}

type reportManifest struct {
	SessionID     string            `json:"session_id"`
	GeneratedAt   string            `json:"generated_at"`
	Redaction     string            `json:"redaction"`
	ImagePayloads string            `json:"image_payloads"`
	Description   string            `json:"description"`
	Severity      string            `json:"severity"`
	Marker        *UserReportMarker `json:"marker"`
	Stats         manifestStats     `json:"stats"`
}

func ScrubCredentials(text string) string {
	if text == "" {
		return text
	}
	for _, pattern := range credentialPatterns {
		text = pattern.re.ReplaceAllString(text, pattern.repl)
	}
	return text
}

func SeverityValid(severity string) bool {
	for _, candidate := range Severities {
		if severity == candidate {
			return true
		}
	}
	return false
}

func NowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func WriteUserReportMarker(sessionDir, description, severity string) (UserReportMarker, error) {
	if !SeverityValid(severity) {
		severity = "medium"
	}
	marker := UserReportMarker{
		Label:       "bad",
		LabelSource: "user_reported",
		Description: description,
		Severity:    severity,
		ReportedAt:  NowISO(),
		Reporter:    "desktop",
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return marker, err
	}
	data, err := marshalIndent(marker)
	if err != nil {
		return marker, err
	}
	if err := os.WriteFile(filepath.Join(sessionDir, MarkerName), data, 0o644); err != nil {
		return marker, err
	}
	return marker, nil
}

func BuildSessionReportBundle(sessionDir, sessionID, description, severity string, marker *UserReportMarker) ([]byte, BundleStats, error) {
	stats := BundleStats{OmittedPaths: []string{}}
	if !validArchiveSessionID(sessionID) {
		return nil, stats, fmt.Errorf("invalid session id %q", sessionID)
	}
	var uncompressed bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&uncompressed, gzip.BestCompression)
	if err != nil {
		return nil, stats, err
	}
	tarWriter := tar.NewWriter(gzipWriter)

	files, err := collectSessionFiles(sessionDir)
	if err != nil {
		tarWriter.Close()
		gzipWriter.Close()
		return nil, stats, err
	}

	binaryUsed := int64(0)
	for _, rel := range files {
		full := filepath.Join(sessionDir, filepath.FromSlash(rel))
		arcname := "session_" + sessionID + "/" + rel
		stats.Files++

		if _, isText := textSuffixes[strings.ToLower(filepath.Ext(rel))]; isText {
			raw, readErr := os.ReadFile(full)
			if readErr != nil {
				continue
			}
			scrubbed := ScrubCredentials(strings.ToValidUTF8(string(raw), "\uFFFD"))
			data := []byte(scrubbed)
			if addErr := addBytes(tarWriter, arcname, data); addErr != nil {
				tarWriter.Close()
				gzipWriter.Close()
				return nil, stats, addErr
			}
			stats.TextFiles++
			stats.BytesUncompressed += len(data)
			continue
		}

		info, statErr := os.Stat(full)
		if statErr != nil {
			continue
		}
		size := info.Size()
		if size > maxSingleBinary || binaryUsed+size > binaryBudgetBytes {
			stats.BinaryOmitted++
			stats.OmittedPaths = append(stats.OmittedPaths, rel)
			continue
		}
		if addErr := addFile(tarWriter, full, arcname, info); addErr != nil {
			continue
		}
		binaryUsed += size
		stats.BinaryFiles++
		stats.BytesUncompressed += int(size)
	}

	omitted := stats.OmittedPaths
	if omitted == nil {
		omitted = []string{}
	}
	if len(omitted) > 50 {
		omitted = omitted[:50]
	}
	manifest := reportManifest{
		SessionID:     sessionID,
		GeneratedAt:   NowISO(),
		Redaction:     "credentials_only",
		ImagePayloads: "stripped (size control; full pixels remain on disk)",
		Description:   description,
		Severity:      severity,
		Marker:        marker,
		Stats: manifestStats{
			Files:          stats.Files,
			TextFiles:      stats.TextFiles,
			BinaryFiles:    stats.BinaryFiles,
			BinaryOmitted:  stats.BinaryOmitted,
			ImagesStripped: stats.ImagesStripped,
			OmittedPaths:   omitted,
		},
	}
	manifestJSON, err := marshalIndent(manifest)
	if err != nil {
		tarWriter.Close()
		gzipWriter.Close()
		return nil, stats, err
	}
	if err := addBytes(tarWriter, "session_"+sessionID+"/report_manifest.json", manifestJSON); err != nil {
		tarWriter.Close()
		gzipWriter.Close()
		return nil, stats, err
	}
	if err := tarWriter.Close(); err != nil {
		gzipWriter.Close()
		return nil, stats, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, stats, err
	}
	return uncompressed.Bytes(), stats, nil
}

func Sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// validArchiveSessionID keeps a caller-supplied id from shaping an archive
// member name (it prefixes every entry in the bundle).
func validArchiveSessionID(sessionID string) bool {
	if sessionID == "" || len(sessionID) > 255 {
		return false
	}
	if sessionID == "." || sessionID == ".." {
		return false
	}
	return !strings.ContainsAny(sessionID, "/\\\x00")
}

func collectSessionFiles(sessionDir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(sessionDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		// Info() reports the link itself, not its target: a symlink planted in
		// the session dir must not pull an outside file into the bundle.
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(sessionDir, path)
		if relErr != nil {
			return nil
		}
		slash := filepath.ToSlash(rel)
		parts := strings.Split(slash, "/")
		if len(parts) >= 2 && parts[0] == "data" && parts[1] == "reports" {
			return nil
		}
		files = append(files, slash)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func addBytes(tw *tar.Writer, arcname string, data []byte) error {
	header := &tar.Header{
		Name:     arcname,
		Size:     int64(len(data)),
		Mode:     0o644,
		ModTime:  time.Unix(time.Now().Unix(), 0),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func addFile(tw *tar.Writer, path, arcname string, info os.FileInfo) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = arcname
	header.Typeflag = tar.TypeReg
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(tw, file)
	return err
}

func marshalIndent(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return escapeNonASCII(data), nil
}

func escapeNonASCII(data []byte) []byte {
	var out bytes.Buffer
	for _, r := range string(data) {
		if r < 0x80 {
			out.WriteByte(byte(r))
			continue
		}
		if r <= 0xFFFF {
			fmt.Fprintf(&out, "\\u%04x", r)
			continue
		}
		high, low := utf16.EncodeRune(r)
		fmt.Fprintf(&out, "\\u%04x\\u%04x", high, low)
	}
	return out.Bytes()
}
