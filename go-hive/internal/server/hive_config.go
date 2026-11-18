package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// Parity: framework/config.py::get_hive_config
func HiveConfig(home hivehome.Home) map[string]any {
	data, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		return map[string]any{}
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil || out == nil {
		return map[string]any{}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return map[string]any{}
	}
	return out
}

// Parity: framework/config.py::get_stall_check_enabled
func StallCheckEnabled(home hivehome.Home) bool {
	if raw, ok := os.LookupEnv("HIVE_STALL_CHECK"); ok {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			switch strings.ToLower(trimmed) {
			case "0", "false", "no", "off":
				return false
			}
			return true
		}
	}
	return pyTruthy(HiveConfig(home)["stall_check"])
}

func pyTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		f, err := v.Float64()
		return err != nil || f != 0
	case float64:
		return v != 0
	case float32:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func pyStr(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	case json.Number:
		return v.String()
	case float64:
		s := strconv.FormatFloat(v, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") && !strings.Contains(s, "Inf") && !strings.Contains(s, "NaN") {
			s += ".0"
		}
		return s
	case float32:
		return pyStr(float64(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return fmt.Sprintf("%v", value)
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}
