package tools

import (
	"fmt"
	"io"

	"github.com/ledongthuc/pdf"
)

func pdfFilterName(v pdf.Value) string {
	filter := v.Key("Filter")
	switch filter.Kind() {
	case pdf.Name:
		return filter.Name()
	case pdf.Array:
		if filter.Len() > 0 {
			return filter.Index(0).Name()
		}
	}
	return ""
}

// pdfStreamBytes returns decoded stream bytes for image XObjects.
func pdfStreamBytes(pdfPath string, v pdf.Value) ([]byte, error) {
	if v.Kind() != pdf.Stream {
		return nil, fmt.Errorf("not a stream")
	}
	name := pdfFilterName(v)
	switch name {
	case "DCTDecode":
		return pdfRawStreamBytes(pdfPath, v)
	case "", "Identity":
		return readAllPDFStream(v)
	case "FlateDecode":
		if data, err := readAllPDFStream(v); err == nil && len(data) > 0 {
			return data, nil
		}
		return pdfRawStreamBytes(pdfPath, v)
	case "CCITTFaxDecode":
		return pdfRawStreamBytes(pdfPath, v)
	default:
		return nil, fmt.Errorf("unsupported stream filter %q", name)
	}
}

func readAllPDFStream(v pdf.Value) ([]byte, error) {
	rc := v.Reader()
	if rc == nil {
		return nil, fmt.Errorf("no stream reader")
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// pdfRawStreamBytes returns a stream's bytes before its filter is undone (the
// JPEG bytes of a DCTDecode image, say). The library decrypts them first, so
// this also works on locked PDFs that open without a password; reading the
// bytes straight from the file gave scrambled images for those.
func pdfRawStreamBytes(pdfPath string, v pdf.Value) ([]byte, error) {
	length := v.Key("Length").Int64()
	if length <= 0 || length > 64<<20 {
		return nil, fmt.Errorf("bad stream length")
	}
	return v.RawBytes(64 << 20)
}

func isJPEG(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
}

func imageExtForBytes(data []byte, filter string) string {
	if filter == "DCTDecode" || isJPEG(data) {
		return ".jpg"
	}
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 0x50 {
		return ".png"
	}
	return ".bin"
}
