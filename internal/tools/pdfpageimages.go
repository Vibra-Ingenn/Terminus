package tools

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"github.com/ledongthuc/pdf"
)

type pageImageCandidate struct {
	width  int
	height int
	path   string
}

// extractPDFPageImagePaths pulls the largest embedded image per page into dir.
// Returns one path per page (empty string if none found for that page).
func extractPDFPageImagePaths(pdfPath, dir string) ([]string, error) {
	openPath, cleanup, f, r, err := openPDFWithRepair(pdfPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanup != nil {
			cleanup()
		}
		if f != nil {
			_ = f.Close()
		}
	}()

	total := r.NumPage()
	out := make([]string, total)
	for pageNum := 1; pageNum <= total; pageNum++ {
		page := r.Page(pageNum)
		if page.V.IsNull() {
			continue
		}
		best, err := largestXObjectImage(page, openPath, dir, pageNum)
		if err != nil {
			continue
		}
		if best != nil {
			out[pageNum-1] = best.path
		}
	}
	return out, nil
}

func largestXObjectImage(page pdf.Page, pdfPath, dir string, pageNum int) (*pageImageCandidate, error) {
	xobj := page.Resources().Key("XObject")
	if xobj.IsNull() || len(xobj.Keys()) == 0 {
		return nil, fmt.Errorf("no xobject dict")
	}
	var best *pageImageCandidate
	for _, key := range xobj.Keys() {
		obj := xobj.Key(key)
		if obj.Key("Subtype").Name() != "Image" {
			continue
		}
		w := int(obj.Key("Width").Int64())
		h := int(obj.Key("Height").Int64())
		if w <= 0 || h <= 0 {
			continue
		}
		data, err := pdfStreamBytes(pdfPath, obj)
		if err != nil || len(data) == 0 {
			continue
		}
		filter := pdfFilterName(obj)
		ext := imageExtForBytes(data, filter)
		if ext == ".bin" || filter == "CCITTFaxDecode" || int(obj.Key("BitsPerComponent").Int64()) == 1 {
			pngPath, err := imageXObjectToPNG(pdfPath, obj, dir, pageNum, key)
			if err != nil {
				pngPath, err = flateImageToPNG(pdfPath, obj, dir, pageNum, key)
			}
			if err != nil {
				continue
			}
			area := w * h
			if best == nil || area > best.width*best.height {
				best = &pageImageCandidate{width: w, height: h, path: pngPath}
			}
			continue
		}
		path := filepath.Join(dir, fmt.Sprintf("page-%d-%s%s", pageNum, key, ext))
		if err := os.WriteFile(path, data, 0644); err != nil {
			continue
		}
		area := w * h
		if best == nil || area > best.width*best.height {
			best = &pageImageCandidate{width: w, height: h, path: path}
		}
	}
	return best, nil
}

func flateImageToPNG(pdfPath string, obj pdf.Value, dir string, pageNum int, key string) (string, error) {
	w := int(obj.Key("Width").Int64())
	h := int(obj.Key("Height").Int64())
	bpc := int(obj.Key("BitsPerComponent").Int64())
	if bpc == 0 {
		bpc = 8
	}
	cs := obj.Key("ColorSpace").Name()
	if cs == "" && obj.Key("ColorSpace").Len() > 0 {
		cs = obj.Key("ColorSpace").Index(0).Name()
	}
	data, err := pdfStreamBytes(pdfPath, obj)
	if err != nil || len(data) == 0 {
		return "", fmt.Errorf("no image bytes")
	}
	components := 1
	switch cs {
	case "DeviceRGB":
		components = 3
	case "DeviceCMYK":
		components = 4
	default:
		components = 1
	}
	if len(data) < w*h*components*(bpc/8) {
		return "", fmt.Errorf("short image data")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	idx := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var gray uint8
			if components == 3 && idx+2 < len(data) {
				r, g, b := data[idx], data[idx+1], data[idx+2]
				idx += 3
				img.Set(x, y, color.RGBA{r, g, b, 255})
				continue
			}
			if idx < len(data) {
				gray = data[idx]
				idx++
			}
			img.Set(x, y, color.RGBA{gray, gray, gray, 255})
		}
	}
	path := filepath.Join(dir, fmt.Sprintf("page-%d-%s.png", pageNum, key))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	err = png.Encode(f, img)
	_ = f.Close()
	if err != nil {
		return "", err
	}
	return path, nil
}
