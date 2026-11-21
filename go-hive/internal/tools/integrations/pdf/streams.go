package pdf

// Stream decoding: FlateDecode / ASCIIHexDecode filters, PNG predictors and
// the bounded inflate helper.

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"io"
)

func (d *pdfDocument) decodeStream(stream *pdfStream) ([]byte, error) {
	data := stream.raw
	filters := d.resolve(stream.dict["Filter"])
	var names []string
	switch typed := filters.(type) {
	case pdfName:
		names = []string{string(typed)}
	case []any:
		for _, item := range typed {
			if name, ok := d.resolve(item).(pdfName); ok {
				names = append(names, string(name))
			}
		}
	}
	for _, name := range names {
		switch name {
		case "FlateDecode", "Fl":
			decoded, err := pdfInflate(data)
			if err != nil {
				return nil, err
			}
			data = decoded
		case "ASCIIHexDecode", "AHx":
			data = pdfASCIIHexDecode(data)
		default:
			return nil, fmt.Errorf("%w: unsupported stream filter %s", errPDFInvalid, name)
		}
	}
	return d.applyPredictor(data, stream.dict["DecodeParms"]), nil
}

// applyPredictor undoes the PNG predictors (10-15) used by xref streams and
// occasional content streams. Other predictor values need no work.
func (d *pdfDocument) applyPredictor(data []byte, parms any) []byte {
	parmsValue := d.resolve(parms)
	if array, ok := parmsValue.([]any); ok && len(array) > 0 {
		parmsValue = d.resolve(array[0])
	}
	dict := d.dict(parmsValue)
	if dict == nil {
		return data
	}
	predictor := d.integer(dict["Predictor"])
	if predictor < 10 || predictor > 15 || len(data) == 0 {
		return data
	}
	columns := d.integer(dict["Columns"])
	if columns <= 0 {
		return data
	}
	colors := d.integer(dict["Colors"])
	if colors <= 0 {
		colors = 1
	}
	bits := d.integer(dict["BitsPerComponent"])
	if bits <= 0 {
		bits = 8
	}
	rowLength := (columns*colors*bits + 7) / 8
	bytesPerPixel := (colors*bits + 7) / 8
	if bytesPerPixel < 1 {
		bytesPerPixel = 1
	}
	out := make([]byte, 0, len(data))
	previous := make([]byte, rowLength)
	cursor := 0
	for cursor < len(data) && cursor+1+rowLength <= len(data) {
		filter := data[cursor]
		cursor++
		row := append([]byte(nil), data[cursor:cursor+rowLength]...)
		cursor += rowLength
		switch filter {
		case 0: // None
		case 1: // Sub
			for i := bytesPerPixel; i < len(row); i++ {
				row[i] += row[i-bytesPerPixel]
			}
		case 2: // Up
			for i := 0; i < len(row); i++ {
				row[i] += previous[i]
			}
		case 3: // Average
			for i := 0; i < len(row); i++ {
				left := 0
				if i >= bytesPerPixel {
					left = int(row[i-bytesPerPixel])
				}
				row[i] += byte((left + int(previous[i])) / 2)
			}
		case 4: // Paeth
			for i := 0; i < len(row); i++ {
				left, upperLeft := 0, 0
				if i >= bytesPerPixel {
					left = int(row[i-bytesPerPixel])
					upperLeft = int(previous[i-bytesPerPixel])
				}
				row[i] += byte(pdfPaeth(left, int(previous[i]), upperLeft))
			}
		default:
			return data
		}
		out = append(out, row...)
		previous = row
	}
	return out
}

func pdfPaeth(left, above, upperLeft int) int {
	estimate := left + above - upperLeft
	leftDistance := estimate - left
	if leftDistance < 0 {
		leftDistance = -leftDistance
	}
	aboveDistance := estimate - above
	if aboveDistance < 0 {
		aboveDistance = -aboveDistance
	}
	upperLeftDistance := estimate - upperLeft
	if upperLeftDistance < 0 {
		upperLeftDistance = -upperLeftDistance
	}
	if leftDistance <= aboveDistance && leftDistance <= upperLeftDistance {
		return left
	}
	if aboveDistance <= upperLeftDistance {
		return above
	}
	return upperLeft
}

func pdfInflate(data []byte) ([]byte, error) {
	if reader, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		defer reader.Close()
		return io.ReadAll(io.LimitReader(reader, maxDecodedStream))
	}
	reader := flate.NewReader(bytes.NewReader(data))
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, maxDecodedStream))
}

func pdfASCIIHexDecode(data []byte) []byte {
	out := make([]byte, 0, len(data)/2)
	high := -1
	for _, char := range data {
		if char == '>' {
			break
		}
		value := -1
		switch {
		case char >= '0' && char <= '9':
			value = int(char - '0')
		case char >= 'a' && char <= 'f':
			value = int(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value = int(char-'A') + 10
		}
		if value < 0 {
			continue
		}
		if high < 0 {
			high = value
			continue
		}
		out = append(out, byte(high<<4|value))
		high = -1
	}
	if high >= 0 {
		out = append(out, byte(high<<4))
	}
	return out
}
