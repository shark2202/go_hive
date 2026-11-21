package pdf

// The Info dictionary projection and the small value/string helpers shared by
// the parser and the tool response.

import (
	"strconv"
	"strings"
	"unicode/utf16"
)

func (d *pdfDocument) metadata() map[string]any {
	info := d.dict(d.trailer["Info"])
	result := map[string]any{
		"title": nil, "author": nil, "subject": nil, "creator": nil,
		"producer": nil, "created": nil, "modified": nil,
	}
	if info == nil {
		return result
	}
	fields := map[string]string{
		"Title": "title", "Author": "author", "Subject": "subject",
		"Creator": "creator", "Producer": "producer",
		"CreationDate": "created", "ModDate": "modified",
	}
	for key, field := range fields {
		if text, ok := pdfTextString(info[key]); ok && text != "" {
			result[field] = text
		}
	}
	return result
}

func pdfTextString(value any) (string, bool) {
	raw, ok := value.(string)
	if !ok {
		return "", false
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		units := make([]uint16, 0, (len(raw)-2)/2)
		for i := 2; i+1 < len(raw); i += 2 {
			units = append(units, uint16(raw[i])<<8|uint16(raw[i+1]))
		}
		return string(utf16.Decode(units)), true
	}
	return pdfLatin1String(raw), true
}

func pdfIntValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	}
	return 0
}

func pdfNameOf(value any) string {
	name, _ := value.(pdfName)
	return string(name)
}

func pdfNormalizeText(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}
