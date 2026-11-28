package image

// PNG/JPEG re-encode step: Pillow's denoise/unsharp passes have no stdlib
// equivalent, but saving from pixels still drops ancillary metadata
// (C2PA/EXIF/XMP) exactly like upstream's "save from pixels" step. WebP has no
// stdlib codec, so its bytes are kept.

import (
	"bytes"
	"image/jpeg"
	"image/png"
)

func postprocessImage(raw []byte, format string) []byte {
	switch format {
	case "png":
		decoded, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return raw
		}
		var out bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		if err := encoder.Encode(&out, decoded); err != nil {
			return raw
		}
		return out.Bytes()
	case "jpeg":
		decoded, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			return raw
		}
		var out bytes.Buffer
		if err := jpeg.Encode(&out, decoded, &jpeg.Options{Quality: 95}); err != nil {
			return raw
		}
		return out.Bytes()
	default:
		return raw
	}
}
