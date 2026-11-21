package pdf

// Page tree walking plus per-page content and text extraction.

import (
	"bytes"
	"fmt"
)

func (d *pdfDocument) pages() ([]pdfPage, error) {
	root := d.dict(d.trailer["Root"])
	if root == nil {
		return nil, fmt.Errorf("%w: missing catalog", errPDFInvalid)
	}
	tree := d.dict(root["Pages"])
	if tree == nil {
		return nil, fmt.Errorf("%w: missing page tree", errPDFInvalid)
	}
	pages := make([]pdfPage, 0, 8)
	visiting := map[pdfRef]bool{}
	var walk func(node map[string]any, inherited map[string]any, depth int)
	walk = func(node map[string]any, inherited map[string]any, depth int) {
		if node == nil || depth > 64 || len(pages) > 100_000 {
			return
		}
		resources := d.dict(node["Resources"])
		if resources == nil {
			resources = inherited
		}
		kids := d.array(node["Kids"])
		isPage := pdfNameOf(node["Type"]) == "Page"
		if !isPage && kids != nil {
			for _, kid := range kids {
				if ref, ok := kid.(pdfRef); ok {
					if visiting[ref] {
						continue
					}
					visiting[ref] = true
				}
				walk(d.dict(kid), resources, depth+1)
			}
			return
		}
		pages = append(pages, pdfPage{dict: node, resources: resources})
	}
	walk(tree, nil, 0)
	if len(pages) == 0 {
		return nil, fmt.Errorf("%w: no pages", errPDFInvalid)
	}
	return pages, nil
}

func (d *pdfDocument) pageText(page pdfPage) (string, error) {
	content, err := d.pageContent(page)
	if err != nil {
		return "", err
	}
	fonts := d.pageFonts(page.resources)
	return pdfExtractText(content, fonts), nil
}

func (d *pdfDocument) pageContent(page pdfPage) ([]byte, error) {
	contents := d.resolve(page.dict["Contents"])
	switch typed := contents.(type) {
	case *pdfStream:
		return d.decodeStream(typed)
	case []any:
		var buffer bytes.Buffer
		for _, item := range typed {
			stream, ok := d.resolve(item).(*pdfStream)
			if !ok {
				continue
			}
			decoded, err := d.decodeStream(stream)
			if err != nil {
				return nil, err
			}
			buffer.Write(decoded)
			buffer.WriteByte('\n')
		}
		return buffer.Bytes(), nil
	}
	return nil, nil
}
