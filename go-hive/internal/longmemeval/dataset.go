package longmemeval

// LongMemEval dataset loading and splitting.
//
// Parity: tests/e2e/longmemeval.py::ensure_split
//
// The 2025 cleaned release is one JSON array; S is ~277 MB and M is ~2.7 GB.
// Elements are streamed one at a time into one file per question plus an
// index, exactly as the Python harness does, so later runs only read the
// questions they run.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	VariantS = "s"
	VariantM = "m"

	// DataURLTemplate is the 2025 cleaned release on HuggingFace.
	DataURLTemplate = "https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_%s_cleaned.json"

	// EnvData names the directory holding the raw and split dataset, as in
	// upstream HIVE_LME_DATA.
	EnvData = "HIVE_LME_DATA"
)

// Item is one LongMemEval question with its haystack of past sessions.
type Item struct {
	QuestionID         string   `json:"question_id"`
	QuestionType       string   `json:"question_type"`
	Question           string   `json:"question"`
	Answer             string   `json:"answer"`
	QuestionDate       string   `json:"question_date"`
	HaystackSessionIDs []string `json:"haystack_session_ids"`
	HaystackDates      []string `json:"haystack_dates"`
	HaystackSessions   [][]Turn `json:"haystack_sessions"`
	AnswerSessionIDs   []string `json:"answer_session_ids"`
}

// Turn is one message inside a haystack session.
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// IndexEntry is the per-question summary written to index.json.
type IndexEntry struct {
	QuestionID   string `json:"question_id"`
	QuestionType string `json:"question_type"`
	QuestionDate string `json:"question_date"`
	Sessions     int    `json:"sessions"`
}

// QuestionTypes is the six-type taxonomy the official judge prompt covers.
var QuestionTypes = []string{
	"single-session-user",
	"single-session-assistant",
	"multi-session",
	"temporal-reasoning",
	"knowledge-update",
	"single-session-preference",
}

var questionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// ValidQuestionID reports whether id is safe as a file name and directory
// segment. The official ids are short slugs, some ending in _abs.
func ValidQuestionID(id string) bool { return questionIDPattern.MatchString(id) }

// ValidQuestionType reports whether t is one of the six official types.
func ValidQuestionType(t string) bool {
	for _, known := range QuestionTypes {
		if t == known {
			return true
		}
	}
	return false
}

func rawFileName(variant string) string {
	return fmt.Sprintf("longmemeval_%s_cleaned.json", variant)
}

func splitDirName(variant string) string {
	return fmt.Sprintf("%s_cleaned", variant)
}

// NormalizeVariant validates the S/M selector.
func NormalizeVariant(variant string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(variant))
	if v == "" {
		v = VariantS
	}
	if v != VariantS && v != VariantM {
		return "", fmt.Errorf("dataset must be 's' or 'm', got %q", variant)
	}
	return v, nil
}

// DataURL returns the HuggingFace URL for one variant.
func DataURL(variant string) string {
	return fmt.Sprintf(DataURLTemplate, variant)
}

// DefaultDataDir mirrors upstream: $HIVE_LME_DATA or <cwd>/.cache/longmemeval.
func DefaultDataDir() string {
	if dir := strings.TrimSpace(os.Getenv(EnvData)); dir != "" {
		return dir
	}
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.Join(os.TempDir(), "hive-longmemeval")
	}
	return filepath.Join(cwd, ".cache", "longmemeval")
}

// DefaultCacheDir is where split question files live when the caller does not
// name a directory. It never writes next to a read-only --data file.
func DefaultCacheDir() string {
	if dir := strings.TrimSpace(os.Getenv(EnvData)); dir != "" {
		return dir
	}
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, "hive-longmemeval")
	}
	return filepath.Join(os.TempDir(), "hive-longmemeval")
}

// SourceConfig says where the dataset comes from and where the split goes.
type SourceConfig struct {
	Variant  string
	DataPath string // a raw JSON array file, or a directory holding it
	CacheDir string
	Download bool
	Logf     func(format string, args ...any)
}

// Prepare returns the split directory (index.json + one file per question),
// downloading the raw release when Download is set and it is missing.
func Prepare(ctx context.Context, cfg SourceConfig) (string, error) {
	variant, err := NormalizeVariant(cfg.Variant)
	if err != nil {
		return "", err
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	dataPath := strings.TrimSpace(cfg.DataPath)
	cacheDir := strings.TrimSpace(cfg.CacheDir)
	var rawPath, splitDir string
	switch {
	case dataPath == "":
		dir := DefaultDataDir()
		rawPath = filepath.Join(dir, rawFileName(variant))
		splitDir = filepath.Join(dir, splitDirName(variant))
	default:
		info, statErr := os.Stat(dataPath)
		switch {
		case statErr == nil && info.IsDir():
			if indexExists(dataPath) {
				return dataPath, nil
			}
			rawPath = filepath.Join(dataPath, rawFileName(variant))
			if cacheDir != "" {
				splitDir = filepath.Join(cacheDir, splitDirName(variant))
			} else {
				splitDir = filepath.Join(dataPath, splitDirName(variant))
			}
		case statErr == nil:
			rawPath = dataPath
			if cacheDir == "" {
				cacheDir = DefaultCacheDir()
			}
			splitDir = filepath.Join(cacheDir, splitDirName(variant))
		default:
			return "", fmt.Errorf("dataset path %q: %w", dataPath, statErr)
		}
	}
	if indexExists(splitDir) {
		return splitDir, nil
	}

	if _, statErr := os.Stat(rawPath); statErr != nil {
		if !cfg.Download {
			return "", fmt.Errorf("raw dataset %q is missing (set --download or HIVE_LME_DATA): %w", rawPath, statErr)
		}
		logf("downloading %s -> %s", DataURL(variant), rawPath)
		if err := Download(ctx, DataURL(variant), rawPath); err != nil {
			return "", err
		}
	}

	logf("splitting %s -> %s", rawPath, splitDir)
	if err := Split(rawPath, splitDir); err != nil {
		return "", err
	}
	return splitDir, nil
}

func indexExists(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "index.json"))
	return err == nil && !info.IsDir()
}

// Download streams url into dest via a .part file, replacing dest only on a
// complete read.
func Download(ctx context.Context, url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create dataset dir: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 60 * time.Second}).DialContext,
			ResponseHeaderTimeout: 60 * time.Second,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download dataset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("download dataset: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	part := dest + ".part"
	fh, err := os.Create(part)
	if err != nil {
		return fmt.Errorf("create download file: %w", err)
	}
	if _, err := io.Copy(fh, resp.Body); err != nil {
		fh.Close()
		os.Remove(part)
		return fmt.Errorf("download dataset: %w", err)
	}
	if err := fh.Close(); err != nil {
		os.Remove(part)
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		os.Remove(part)
		return err
	}
	return nil
}

// Split streams a top-level JSON array into one file per question and writes
// index.json last. A truncated file is an error, not a smaller dataset.
//
// Parity: tests/e2e/longmemeval.py::ensure_split
func Split(rawPath, splitDir string) error {
	if err := os.MkdirAll(splitDir, 0o755); err != nil {
		return fmt.Errorf("create split dir: %w", err)
	}
	index := make([]IndexEntry, 0, 512)
	err := streamArray(rawPath, func(raw json.RawMessage) error {
		var item Item
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("decode question: %w", err)
		}
		if err := ValidateItem(&item); err != nil {
			return err
		}
		if err := writeJSONFile(filepath.Join(splitDir, item.QuestionID+".json"), item); err != nil {
			return err
		}
		index = append(index, IndexEntry{
			QuestionID:   item.QuestionID,
			QuestionType: item.QuestionType,
			QuestionDate: item.QuestionDate,
			Sessions:     len(item.HaystackSessions),
		})
		return nil
	})
	if err != nil {
		return err
	}
	if len(index) == 0 {
		return fmt.Errorf("%s holds no questions", rawPath)
	}
	indexPath := filepath.Join(splitDir, "index.json")
	tmp := indexPath + ".tmp"
	encoded, err := json.MarshalIndent(index, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, encoded, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, indexPath)
}

func streamArray(path string, fn func(json.RawMessage) error) error {
	fh, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open dataset: %w", err)
	}
	defer fh.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(fh, 1<<20))
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("read dataset %s: %w", path, err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '[' {
		return fmt.Errorf("%s does not hold a JSON array", path)
	}
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return fmt.Errorf("truncated JSON array in %s: %w", path, err)
		}
		if err := fn(raw); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("truncated JSON array in %s: %w", path, err)
	}
	return nil
}

// ValidateItem checks the fields the harness indexes and seeds with.
func ValidateItem(item *Item) error {
	if !ValidQuestionID(item.QuestionID) {
		return fmt.Errorf("invalid question_id %q", item.QuestionID)
	}
	if !ValidQuestionType(item.QuestionType) {
		return fmt.Errorf("question %s: unknown question_type %q", item.QuestionID, item.QuestionType)
	}
	if strings.TrimSpace(item.Question) == "" {
		return fmt.Errorf("question %s: empty question", item.QuestionID)
	}
	if len(item.HaystackSessionIDs) != len(item.HaystackDates) || len(item.HaystackSessionIDs) != len(item.HaystackSessions) {
		return fmt.Errorf("question %s: haystack arrays disagree (%d ids, %d dates, %d session bodies)",
			item.QuestionID, len(item.HaystackSessionIDs), len(item.HaystackDates), len(item.HaystackSessions))
	}
	for i := range item.HaystackSessionIDs {
		if strings.TrimSpace(item.HaystackSessionIDs[i]) == "" {
			return fmt.Errorf("question %s: empty haystack session id at %d", item.QuestionID, i)
		}
		if _, err := ParseDate(item.HaystackDates[i]); err != nil {
			return fmt.Errorf("question %s: session %s: %w", item.QuestionID, item.HaystackSessionIDs[i], err)
		}
	}
	if _, err := ParseDate(item.QuestionDate); err != nil {
		return fmt.Errorf("question %s: %w", item.QuestionID, err)
	}
	return nil
}

// LoadIndex reads the split index.
func LoadIndex(splitDir string) ([]IndexEntry, error) {
	data, err := os.ReadFile(filepath.Join(splitDir, "index.json"))
	if err != nil {
		return nil, fmt.Errorf("read dataset index: %w", err)
	}
	var index []IndexEntry
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("decode dataset index: %w", err)
	}
	for _, entry := range index {
		if !ValidQuestionID(entry.QuestionID) {
			return nil, fmt.Errorf("dataset index holds invalid question_id %q", entry.QuestionID)
		}
	}
	return index, nil
}

// LoadItem reads one question from the split.
//
// Parity: tests/e2e/longmemeval.py::load_question
func LoadItem(splitDir, questionID string) (Item, error) {
	if !ValidQuestionID(questionID) {
		return Item{}, fmt.Errorf("invalid question_id %q", questionID)
	}
	data, err := os.ReadFile(filepath.Join(splitDir, questionID+".json"))
	if err != nil {
		return Item{}, fmt.Errorf("read question %s: %w", questionID, err)
	}
	var item Item
	if err := json.Unmarshal(data, &item); err != nil {
		return Item{}, fmt.Errorf("decode question %s: %w", questionID, err)
	}
	if err := ValidateItem(&item); err != nil {
		return Item{}, err
	}
	return item, nil
}

// ReadItems decodes a small raw dataset (fixtures and tests).
func ReadItems(rawPath string) ([]Item, error) {
	var items []Item
	err := streamArray(rawPath, func(raw json.RawMessage) error {
		var item Item
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if err := ValidateItem(&item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	})
	return items, err
}

var datePattern = regexp.MustCompile(`\s*\([^)]*\)\s*`)

// ParseDate turns "2023/05/30 (Tue) 23:40" into a local time. The weekday is
// dropped first because %a-style parsing is locale-dependent.
//
// Parity: tests/e2e/longmemeval.py::parse_date
func ParseDate(text string) (time.Time, error) {
	cleaned := strings.TrimSpace(datePattern.ReplaceAllString(text, " "))
	if cleaned == "" {
		return time.Time{}, errors.New("empty date")
	}
	parsed, err := time.ParseInLocation("2006/01/02 15:04", cleaned, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse date %q: %w", text, err)
	}
	return parsed, nil
}

// IsAbstention reports whether questionID is one of the 30 unanswerable
// questions (the official ids carry an _abs suffix).
func IsAbstention(questionID string) bool { return strings.Contains(questionID, "_abs") }

// SortIDs sorts ids for a stable report order.
func SortIDs(ids []string) { sort.Strings(ids) }

func writeJSONFile(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
