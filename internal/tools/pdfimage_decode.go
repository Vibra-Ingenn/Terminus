package tools

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/ledongthuc/pdf"
	"golang.org/x/image/ccitt"
)

func pdfImageBlackIs1(obj pdf.Value) bool {
	decode := obj.Key("Decode")
	if decode.Len() >= 2 {
		return decode.Index(0).Float64() > decode.Index(1).Float64()
	}
	return obj.Key("ImageMask").Bool()
}

func inflateStream(data []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return data, nil
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

func decodeCCITTBitmap(data []byte, w, h int, k int64, blackIs1 bool) ([]byte, error) {
	sf := ccitt.Group3
	if k < 0 {
		sf = ccitt.Group4
	}
	opts := &ccitt.Options{Invert: !blackIs1}
	dec := ccitt.NewReader(bytes.NewReader(data), ccitt.MSB, sf, w, h, opts)
	out := make([]byte, w*h)
	if _, err := io.ReadFull(dec, out); err != nil {
		return nil, err
	}
	return out, nil
}

func bitmap1ToRGBA(data []byte, w, h int, blackIs1 bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rowBytes := (w + 7) / 8
	for y := 0; y < h; y++ {
		row := data[y*rowBytes : (y+1)*rowBytes]
		for x := 0; x < w; x++ {
			byteIdx := x / 8
			bit := 7 - (x % 8)
			on := (row[byteIdx]>>bit)&1 == 1
			if !blackIs1 {
				on = !on
			}
			c := color.RGBA{255, 255, 255, 255}
			if on {
				c = color.RGBA{0, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func imageXObjectToPNG(pdfPath string, obj pdf.Value, dir string, pageNum int, key string) (string, error) {
	w := int(obj.Key("Width").Int64())
	h := int(obj.Key("Height").Int64())
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("bad image size")
	}
	bpc := int(obj.Key("BitsPerComponent").Int64())
	if bpc == 0 {
		bpc = 8
	}
	filter := pdfFilterName(obj)
	blackIs1 := pdfImageBlackIs1(obj)

	var rgba *image.RGBA
	switch filter {
	case "CCITTFaxDecode":
		raw, err := pdfRawStreamBytes(pdfPath, obj)
		if err != nil {
			return "", err
		}
		k := obj.Key("K").Int64()
		bits, err := decodeCCITTBitmap(raw, w, h, k, blackIs1)
		if err != nil {
			return "", err
		}
		rgba = bitmap1ToRGBA(bits, w, h, true)
	case "FlateDecode", "", "Identity":
		raw, err := pdfStreamBytes(pdfPath, obj)
		if err != nil {
			raw, err = pdfRawStreamBytes(pdfPath, obj)
		}
		if err != nil {
			return "", err
		}
		raw, _ = inflateStream(raw)
		if bpc == 1 {
			rgba = bitmap1ToRGBA(raw, w, h, blackIs1)
		} else {
			return flateImageToPNG(pdfPath, obj, dir, pageNum, key)
		}
	default:
		if bpc == 1 {
			raw, err := pdfRawStreamBytes(pdfPath, obj)
			if err != nil {
				return "", err
			}
			rgba = bitmap1ToRGBA(raw, w, h, blackIs1)
		} else {
			return "", fmt.Errorf("unsupported image filter %q", filter)
		}
	}
	path := filepath.Join(dir, fmt.Sprintf("page-%d-%s.png", pageNum, key))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	err = png.Encode(f, rgba)
	_ = f.Close()
	if err != nil {
		return "", err
	}
	return path, nil
}
