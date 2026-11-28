package pdf

// PDF document model and cross-reference parsing: classic xref tables,
// cross-reference streams, object scanning for broken files, object streams
// and the trailer/catalog lookup chain.

import (
	"bytes"
	"fmt"
	"strconv"
)

type pdfRef struct {
	num int
	gen int
}

type pdfName string

type pdfKeyword string

type pdfStream struct {
	dict map[string]any
	raw  []byte
}

type pdfDocument struct {
	data      []byte
	objects   map[pdfRef]any
	trailer   map[string]any
	fontCache map[pdfRef]*pdfFont
}

type pdfPage struct {
	dict      map[string]any
	resources map[string]any
}

func pdfOpen(data []byte) (*pdfDocument, error) {
	return pdfOpenPassword(data, "")
}

// pdfOpenPassword parses data and, when the trailer carries an /Encrypt
// dictionary, authenticates and decrypts its objects with password. A wrong
// or missing password surfaces as errPDFEncrypted.
func pdfOpenPassword(data []byte, password string) (*pdfDocument, error) {
	header := bytes.Index(data, []byte("%PDF-"))
	if header < 0 || header > 1024 {
		return nil, fmt.Errorf("%w: missing %%PDF- header", errPDFInvalid)
	}
	doc := &pdfDocument{data: data, objects: map[pdfRef]any{}, fontCache: map[pdfRef]*pdfFont{}}
	doc.loadXref()
	if len(doc.objects) == 0 {
		doc.objects = map[pdfRef]any{}
		doc.scanObjects()
	}
	if len(doc.trailer) == 0 {
		doc.findTrailer()
	}
	if err := doc.decryptDocument(password); err != nil {
		return nil, err
	}
	doc.expandObjectStreams()
	if _, ok := doc.trailer["Root"]; !ok {
		doc.findCatalog()
	}
	if len(doc.objects) == 0 {
		return nil, fmt.Errorf("%w: no indirect objects", errPDFInvalid)
	}
	return doc, nil
}

type pdfXrefEntry struct {
	typ    int
	offset int
}

func (d *pdfDocument) loadXref() {
	startxref := bytes.LastIndex(d.data, []byte("startxref"))
	if startxref < 0 {
		return
	}
	lexer := &pdfLexer{data: d.data}
	offset, _, ok := lexer.readInteger(lexer.skip(startxref + len("startxref")))
	if !ok {
		return
	}
	seen := map[int]bool{}
	for depth := 0; depth < 64 && offset > 0 && offset < len(d.data) && !seen[offset]; depth++ {
		seen[offset] = true
		entries, trailer, prev := d.parseXrefSection(offset)
		for ref, entry := range entries {
			if entry.typ != 1 {
				continue
			}
			if _, exists := d.objects[ref]; exists {
				continue
			}
			if _, obj, err := d.parseIndirectAt(entry.offset); err == nil {
				d.objects[ref] = obj
			}
		}
		if len(trailer) > 0 && len(d.trailer) == 0 {
			d.trailer = trailer
		}
		if prev <= 0 || prev == offset {
			break
		}
		offset = prev
	}
}

func (d *pdfDocument) parseXrefSection(offset int) (map[pdfRef]pdfXrefEntry, map[string]any, int) {
	entries := map[pdfRef]pdfXrefEntry{}
	if bytes.HasPrefix(d.data[offset:], []byte("xref")) {
		lexer := &pdfLexer{data: d.data}
		pos := lexer.skip(offset + len("xref"))
		for {
			pos = lexer.skip(pos)
			if pos >= len(d.data) {
				return entries, nil, 0
			}
			if bytes.HasPrefix(d.data[pos:], []byte("trailer")) {
				trailerValue, after, err := lexer.parseValue(lexer.skip(pos + len("trailer")))
				if err != nil {
					return entries, nil, 0
				}
				trailer, _ := trailerValue.(map[string]any)
				prev := 0
				if trailer != nil {
					prev = pdfIntValue(trailer["Prev"])
				}
				_ = after
				return entries, trailer, prev
			}
			start, next, ok := lexer.readInteger(pos)
			if !ok {
				return entries, nil, 0
			}
			count, nextCount, ok := lexer.readInteger(lexer.skip(next))
			if !ok {
				return entries, nil, 0
			}
			pos = nextCount
			for i := 0; i < count && i < 1_000_000; i++ {
				pos = lexer.skip(pos)
				entryOffset, nextOffset, ok := lexer.readInteger(pos)
				if !ok {
					return entries, nil, 0
				}
				gen, nextGen, ok := lexer.readInteger(lexer.skip(nextOffset))
				if !ok {
					return entries, nil, 0
				}
				pos = lexer.skip(nextGen)
				if pos < len(d.data) {
					kind := d.data[pos]
					pos++
					if kind == 'n' {
						entries[pdfRef{num: start + i, gen: gen}] = pdfXrefEntry{typ: 1, offset: entryOffset}
					}
				}
			}
		}
	}
	// PDF 1.5+ cross-reference stream.
	_, object, err := d.parseIndirectAt(offset)
	if err != nil {
		return entries, nil, 0
	}
	stream, ok := object.(*pdfStream)
	if !ok || pdfNameOf(stream.dict["Type"]) != "XRef" {
		return entries, nil, 0
	}
	decoded, err := d.decodeStream(stream)
	if err != nil {
		return entries, nil, 0
	}
	widths := d.array(stream.dict["W"])
	if len(widths) < 3 {
		return entries, nil, 0
	}
	w0, w1, w2 := pdfIntValue(d.resolve(widths[0])), pdfIntValue(d.resolve(widths[1])), pdfIntValue(d.resolve(widths[2]))
	if w0 < 0 || w1 < 0 || w2 < 0 || w0+w1+w2 <= 0 {
		return entries, nil, 0
	}
	index := d.array(stream.dict["Index"])
	if len(index) == 0 {
		index = []any{0, pdfIntValue(d.resolve(stream.dict["Size"]))}
	}
	record := w0 + w1 + w2
	cursor := 0
	for pair := 0; pair+1 < len(index); pair += 2 {
		first := pdfIntValue(d.resolve(index[pair]))
		count := pdfIntValue(d.resolve(index[pair+1]))
		for i := 0; i < count && cursor+record <= len(decoded); i++ {
			fields := make([]int, 3)
			position := cursor
			for field := 0; field < 3; field++ {
				width := []int{w0, w1, w2}[field]
				value := 0
				for b := 0; b < width; b++ {
					value = value<<8 | int(decoded[position])
					position++
				}
				fields[field] = value
			}
			cursor += record
			typ := fields[0]
			if w0 == 0 {
				typ = 1
			}
			if typ == 1 {
				entries[pdfRef{num: first + i, gen: fields[2]}] = pdfXrefEntry{typ: 1, offset: fields[1]}
			}
		}
	}
	trailer := stream.dict
	prev := pdfIntValue(trailer["Prev"])
	return entries, trailer, prev
}

// parseIndirectAt parses "num gen obj ..." at an absolute offset.
func (d *pdfDocument) parseIndirectAt(offset int) (pdfRef, any, error) {
	if offset < 0 || offset >= len(d.data) {
		return pdfRef{}, nil, fmt.Errorf("%w: xref offset out of range", errPDFInvalid)
	}
	lexer := &pdfLexer{data: d.data}
	pos := lexer.skip(offset)
	num, next, ok := lexer.readInteger(pos)
	if !ok {
		return pdfRef{}, nil, fmt.Errorf("%w: object header", errPDFInvalid)
	}
	gen, nextGen, ok := lexer.readInteger(lexer.skip(next))
	if !ok {
		return pdfRef{}, nil, fmt.Errorf("%w: object header", errPDFInvalid)
	}
	pos = lexer.skip(nextGen)
	if !bytes.HasPrefix(d.data[pos:], []byte("obj")) {
		return pdfRef{}, nil, fmt.Errorf("%w: object header", errPDFInvalid)
	}
	pos += len("obj")
	value, after, err := lexer.parseValue(pos)
	if err != nil {
		return pdfRef{}, nil, err
	}
	pos = lexer.skip(after)
	if bytes.HasPrefix(d.data[pos:], []byte("stream")) {
		return pdfRef{num: num, gen: gen}, d.readStream(value, pos+len("stream")), nil
	}
	return pdfRef{num: num, gen: gen}, value, nil
}

func (d *pdfDocument) readStream(dictValue any, pos int) *pdfStream {
	dict, _ := dictValue.(map[string]any)
	if dict == nil {
		dict = map[string]any{}
	}
	if pos < len(d.data) && d.data[pos] == '\r' {
		pos++
		if pos < len(d.data) && d.data[pos] == '\n' {
			pos++
		}
	} else if pos < len(d.data) && d.data[pos] == '\n' {
		pos++
	}
	length := -1
	switch value := dict["Length"].(type) {
	case int:
		length = value
	case float64:
		length = int(value)
	case pdfRef:
		if resolved, ok := d.resolve(value).(int); ok {
			length = resolved
		}
	}
	var raw []byte
	if length >= 0 && pos+length <= len(d.data) {
		raw = d.data[pos : pos+length]
	} else {
		end := bytes.Index(d.data[pos:], []byte("endstream"))
		if end < 0 {
			end = len(d.data) - pos
		}
		raw = bytes.TrimRight(d.data[pos:pos+end], "\r\n")
	}
	return &pdfStream{dict: dict, raw: append([]byte(nil), raw...)}
}

// scanObjects rebuilds the object map for PDFs whose xref table is broken
// (or absent). It only accepts "N G obj ... endobj/stream" sequences.
func (d *pdfDocument) scanObjects() {
	lexer := &pdfLexer{data: d.data}
	for pos := 0; pos < len(d.data); {
		index := bytes.Index(d.data[pos:], []byte("obj"))
		if index < 0 {
			return
		}
		at := pos + index
		pos = at + len("obj")
		if at+3 < len(d.data) && !pdfIsDelimiter(d.data[at+3]) {
			continue
		}
		ref, _, ok := pdfObjectHeaderBefore(d.data, at)
		if !ok {
			continue
		}
		value, after, err := lexer.parseValue(lexer.skip(at + len("obj")))
		if err != nil {
			continue
		}
		after = lexer.skip(after)
		if bytes.HasPrefix(d.data[after:], []byte("endobj")) {
			if _, exists := d.objects[ref]; !exists {
				d.objects[ref] = value
			}
			continue
		}
		if bytes.HasPrefix(d.data[after:], []byte("stream")) {
			stream := d.readStream(value, after+len("stream"))
			if _, exists := d.objects[ref]; !exists {
				d.objects[ref] = stream
			}
			continue
		}
	}
}

func pdfObjectHeaderBefore(data []byte, objPos int) (pdfRef, int, bool) {
	i := objPos - 1
	for i >= 0 && pdfIsSpace(data[i]) {
		i--
	}
	endGen := i + 1
	for i >= 0 && pdfIsDigit(data[i]) {
		i--
	}
	if i+1 == endGen {
		return pdfRef{}, 0, false
	}
	gen, err := strconv.Atoi(string(data[i+1 : endGen]))
	if err != nil {
		return pdfRef{}, 0, false
	}
	for i >= 0 && pdfIsSpace(data[i]) {
		i--
	}
	endNum := i + 1
	for i >= 0 && pdfIsDigit(data[i]) {
		i--
	}
	if i+1 == endNum {
		return pdfRef{}, 0, false
	}
	num, err := strconv.Atoi(string(data[i+1 : endNum]))
	if err != nil {
		return pdfRef{}, 0, false
	}
	if i >= 0 && !pdfIsSpace(data[i]) && !pdfIsDelimiter(data[i]) {
		return pdfRef{}, 0, false
	}
	return pdfRef{num: num, gen: gen}, i + 1, true
}

func (d *pdfDocument) expandObjectStreams() {
	for _, object := range d.objects {
		stream, ok := object.(*pdfStream)
		if !ok || pdfNameOf(stream.dict["Type"]) != "ObjStm" {
			continue
		}
		decoded, err := d.decodeStream(stream)
		if err != nil {
			continue
		}
		count := pdfIntValue(d.resolve(stream.dict["N"]))
		first := pdfIntValue(d.resolve(stream.dict["First"]))
		if count <= 0 || first < 0 || first > len(decoded) {
			continue
		}
		lexer := &pdfLexer{data: decoded}
		pos := 0
		numbers := make([]int, 0, count)
		offsets := make([]int, 0, count)
		for i := 0; i < count; i++ {
			number, next, ok := lexer.readInteger(lexer.skip(pos))
			if !ok {
				break
			}
			offset, nextOffset, ok := lexer.readInteger(lexer.skip(next))
			if !ok {
				break
			}
			numbers = append(numbers, number)
			offsets = append(offsets, offset)
			pos = nextOffset
		}
		for i := range numbers {
			ref := pdfRef{num: numbers[i], gen: 0}
			if _, exists := d.objects[ref]; exists {
				continue
			}
			value, _, err := lexer.parseValue(first + offsets[i])
			if err != nil {
				continue
			}
			d.objects[ref] = value
		}
	}
}

func (d *pdfDocument) findTrailer() {
	lexer := &pdfLexer{data: d.data}
	pos := 0
	for {
		index := bytes.Index(d.data[pos:], []byte("trailer"))
		if index < 0 {
			break
		}
		at := pos + index
		value, _, err := lexer.parseValue(lexer.skip(at + len("trailer")))
		if err == nil {
			if trailer, ok := value.(map[string]any); ok {
				d.trailer = trailer
			}
		}
		pos = at + len("trailer")
	}
	if len(d.trailer) > 0 {
		return
	}
	for _, object := range d.objects {
		stream, ok := object.(*pdfStream)
		if !ok || pdfNameOf(stream.dict["Type"]) != "XRef" {
			continue
		}
		if stream.dict["Root"] != nil {
			d.trailer = stream.dict
			return
		}
	}
}

func (d *pdfDocument) findCatalog() {
	for ref, object := range d.objects {
		dict, ok := object.(map[string]any)
		if !ok {
			continue
		}
		if pdfNameOf(dict["Type"]) == "Catalog" {
			if d.trailer == nil {
				d.trailer = map[string]any{}
			}
			d.trailer["Root"] = ref
			return
		}
	}
}

func (d *pdfDocument) resolve(value any) any {
	if ref, ok := value.(pdfRef); ok {
		return d.objects[ref]
	}
	return value
}

func (d *pdfDocument) dict(value any) map[string]any {
	switch typed := d.resolve(value).(type) {
	case map[string]any:
		return typed
	case *pdfStream:
		return typed.dict
	}
	return nil
}

func (d *pdfDocument) array(value any) []any {
	array, _ := d.resolve(value).([]any)
	return array
}

func (d *pdfDocument) integer(value any) int {
	return pdfIntValue(d.resolve(value))
}
