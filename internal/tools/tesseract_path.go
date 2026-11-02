package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func findTesseractExecutable() (string, error) {
	if p := os.Getenv("TESSERACT_CMD"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("tesseract"); err == nil {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		candidates := []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Tesseract-OCR", "tesseract.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Tesseract-OCR", "tesseract.exe"),
			`C:\Program Files\Tesseract-OCR\tesseract.exe`,
		}
		for _, c := range candidates {
			if c == "" {
				continue
			}
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
	}
	return "", fmt.Errorf("Tesseract is not installed or not on PATH")
}
