package pdf

// Font handling: ToUnicode CMaps, WinAnsiEncoding and the UTF-16 helpers used
// by metadata strings.

import (
	"strings"
	"unicode/utf16"
)

type pdfFont struct {
	twoByte   bool
	toUnicode map[uint32]string
}

func (d *pdfDocument) pageFonts(resources map[string]any) map[string]*pdfFont {
	fonts := map[string]*pdfFont{}
	fontDict := d.dict(resources["Font"])
	if fontDict == nil {
		return fonts
	}
	for key, raw := range fontDict {
		font := &pdfFont{}
		dict := d.dict(raw)
		if dict != nil {
			if pdfNameOf(dict["Subtype"]) == "Type0" {
				font.twoByte = true
			}
			if stream, ok := d.resolve(dict["ToUnicode"]).(*pdfStream); ok {
				if decoded, err := d.decodeStream(stream); err == nil {
					mapping, codeBytes := pdfParseCMap(decoded)
					font.toUnicode = mapping
					if codeBytes == 2 {
						font.twoByte = true
					}
				}
			}
		}
		fonts[key] = font
	}
	return fonts
}

// pdfParseCMap reads the bfchar/bfrange sections of a ToUnicode CMap and
// returns the code→unicode map plus the source code width in bytes.
func pdfParseCMap(data []byte) (map[uint32]string, int) {
	mapping := map[uint32]string{}
	codeBytes := 0
	lexer := &pdfLexer{data: data}
	mode := ""
	operands := make([]any, 0, 8)
	flush := func() {
		switch mode {
		case "bfchar":
			for len(operands) >= 2 {
				pdfCMapChar(mapping, operands[0], operands[1], &codeBytes)
				operands = operands[2:]
			}
		case "bfrange":
			for len(operands) >= 3 {
				pdfCMapRange(mapping, operands[0], operands[1], operands[2], &codeBytes)
				operands = operands[3:]
			}
		case "codespace":
			for len(operands) >= 2 {
				if source, ok := operands[0].(string); ok && len(source) > codeBytes {
					codeBytes = len(source)
				}
				operands = operands[2:]
			}
		}
	}
	pos := 0
	for pos < len(data) {
		value, next, err := lexer.parseValue(pos)
		if err != nil {
			break
		}
		pos = next
		if keyword, ok := value.(pdfKeyword); ok {
			switch string(keyword) {
			case "beginbfchar":
				mode, operands = "bfchar", operands[:0]
			case "beginbfrange":
				mode, operands = "bfrange", operands[:0]
			case "begincodespacerange":
				mode, operands = "codespace", operands[:0]
			case "endbfchar", "endbfrange", "endcodespacerange":
				flush()
				mode, operands = "", operands[:0]
			default:
				flush()
				operands = operands[:0]
			}
			continue
		}
		operands = append(operands, value)
		flush()
	}
	return mapping, codeBytes
}

func pdfCMapChar(mapping map[uint32]string, source, destination any, codeBytes *int) {
	sourceText, ok := source.(string)
	if !ok {
		return
	}
	destinationText, ok := destination.(string)
	if !ok {
		return
	}
	if len(sourceText) > *codeBytes {
		*codeBytes = len(sourceText)
	}
	mapping[pdfCodeValue([]byte(sourceText))] = pdfCMapDestination([]byte(destinationText))
}

func pdfCMapRange(mapping map[uint32]string, low, high, destination any, codeBytes *int) {
	lowText, ok := low.(string)
	if !ok {
		return
	}
	highText, ok := high.(string)
	if !ok {
		return
	}
	if len(lowText) > *codeBytes {
		*codeBytes = len(lowText)
	}
	from := pdfCodeValue([]byte(lowText))
	to := pdfCodeValue([]byte(highText))
	if to < from || to-from > 65535 {
		return
	}
	if array, ok := destination.([]any); ok {
		for index, item := range array {
			text, ok := item.(string)
			if !ok {
				break
			}
			mapping[from+uint32(index)] = pdfCMapDestination([]byte(text))
		}
		return
	}
	base, ok := destination.(string)
	if !ok {
		return
	}
	bytes := []byte(base)
	for code := from; code <= to; code++ {
		mapping[code] = pdfCMapDestination(pdfIncrementUTF16(bytes, int(code-from)))
	}
}

func pdfCodeValue(data []byte) uint32 {
	var value uint32
	for _, char := range data {
		value = value<<8 | uint32(char)
	}
	return value
}

func pdfCMapDestination(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if len(data) == 1 {
		return string(pdfWinAnsiRune(data[0]))
	}
	if len(data)%2 == 1 {
		return pdfLatin1String(string(data))
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
	}
	return string(utf16.Decode(units))
}

// pdfIncrementUTF16 adds delta to the last UTF-16 code unit of base.
func pdfIncrementUTF16(base []byte, delta int) []byte {
	if len(base) < 2 {
		return base
	}
	out := append([]byte(nil), base...)
	last := len(out) - 2
	value := int(out[last])<<8 | int(out[last+1])
	value += delta
	if value > 0xFFFF {
		value = 0xFFFF
	}
	out[last] = byte(value >> 8)
	out[last+1] = byte(value)
	return out
}

func pdfDecodeFontString(font *pdfFont, raw string) string {
	if font == nil {
		return pdfLatin1String(raw)
	}
	var builder strings.Builder
	if font.twoByte {
		for i := 0; i+1 < len(raw); i += 2 {
			code := uint32(raw[i])<<8 | uint32(raw[i+1])
			if text, ok := font.toUnicode[code]; ok {
				builder.WriteString(text)
			} else if code >= 0x20 && code != 0xFFFD {
				builder.WriteRune(rune(code))
			}
		}
		return builder.String()
	}
	for i := 0; i < len(raw); i++ {
		code := uint32(raw[i])
		if text, ok := font.toUnicode[code]; ok {
			builder.WriteString(text)
		} else {
			builder.WriteRune(pdfWinAnsiRune(raw[i]))
		}
	}
	return builder.String()
}

func pdfWinAnsiRune(char byte) rune {
	switch char {
	case 0x80:
		return '€'
	case 0x82:
		return '‚'
	case 0x83:
		return 'ƒ'
	case 0x84:
		return '„'
	case 0x85:
		return '…'
	case 0x86:
		return '†'
	case 0x87:
		return '‡'
	case 0x88:
		return 'ˆ'
	case 0x89:
		return '‰'
	case 0x8A:
		return 'Š'
	case 0x8B:
		return '‹'
	case 0x8C:
		return 'Œ'
	case 0x8E:
		return 'Ž'
	case 0x91:
		return '‘'
	case 0x92:
		return '’'
	case 0x93:
		return '“'
	case 0x94:
		return '”'
	case 0x95:
		return '•'
	case 0x96:
		return '–'
	case 0x97:
		return '—'
	case 0x98:
		return '˜'
	case 0x99:
		return '™'
	case 0x9A:
		return 'š'
	case 0x9B:
		return '›'
	case 0x9C:
		return 'œ'
	case 0x9E:
		return 'ž'
	case 0x9F:
		return 'Ÿ'
	}
	return rune(char)
}

func pdfLatin1String(raw string) string {
	var builder strings.Builder
	for i := 0; i < len(raw); i++ {
		builder.WriteRune(rune(raw[i]))
	}
	return builder.String()
}
