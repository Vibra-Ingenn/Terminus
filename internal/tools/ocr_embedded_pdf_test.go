package tools

import (
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-pdf/fpdf"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

func writeLabelJPEG(path, label string) error {
	img := image.NewRGBA(image.Rect(0, 0, 480, 96))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	d := &font.Drawer{
		Dst:  img,
		Src:  image.Black,
		Face: basicfont.Face7x13,
	}
	d.Dot = fixed.P(12, 48)
	d.DrawString(label)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 92})
}

func makeImageOnlyScanPDF(dir string) (string, error) {
	jpgPath := filepath.Join(dir, "scan-page.jpg")
	if err := writeLabelJPEG(jpgPath, "TERMINUS-SCAN-TEST-99"); err != nil {
		return "", err
	}
	pdfPath := filepath.Join(dir, "scan.pdf")
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.ImageOptions(jpgPath, 10, 10, 180, 36, false, fpdf.ImageOptions{ImageType: "JPEG", ReadDpi: true}, 0, "")
	if err := pdf.OutputFileAndClose(pdfPath); err != nil {
		return "", err
	}
	return pdfPath, nil
}

func pathWithoutPoppler() string {
	parts := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
	keep := make([]string, 0, len(parts))
	for _, p := range parts {
		low := strings.ToLower(p)
		if strings.Contains(low, "poppler") {
			continue
		}
		keep = append(keep, p)
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

func TestExtractPDFPageImagePaths_ImageOnlyPDF(t *testing.T) {
	dir := t.TempDir()
	pdfPath, err := makeImageOnlyScanPDF(dir)
	if err != nil {
		t.Fatal(err)
	}
	imgDir := filepath.Join(dir, "imgs")
	if err := os.MkdirAll(imgDir, 0755); err != nil {
		t.Fatal(err)
	}
	paths, err := extractPDFPageImagePaths(pdfPath, imgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 || paths[0] == "" {
		t.Fatalf("expected embedded page image path, got %v", paths)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatalf("image file missing: %v", err)
	}
}

func TestOCRPDFPages_EmbeddedImagesWithoutPopplerOnPath(t *testing.T) {
	if _, err := findTesseractExecutable(); err != nil {
		t.Skip("Tesseract not installed")
	}
	dir := t.TempDir()
	pdfPath, err := makeImageOnlyScanPDF(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	_ = os.Setenv("PATH", pathWithoutPoppler())
	if _, err := findPdftoppmExecutable(); err == nil {
		t.Skip("Poppler still visible (bundled next to test binary); run from a folder without poppler\\")
	}
	res := ocrPDFPages(pdfPath, "eng", "ocr_extract")
	if !res.Success {
		t.Fatalf("ocr failed: %s", res.Error)
	}
	up := strings.ToUpper(res.Output)
	if !strings.Contains(up, "TERMINUS") || !strings.Contains(up, "SCAN") {
		t.Fatalf("unexpected OCR output: %s", res.Output)
	}
}
