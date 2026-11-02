package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

func findPdftoppmExecutable() (string, error) {
	if p := os.Getenv("PDFTOPPM_CMD"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if dir := executableDir(); dir != "" {
		bundled := filepath.Join(dir, "poppler", "pdftoppm.exe")
		if _, err := os.Stat(bundled); err == nil {
			return bundled, nil
		}
		bundledUnix := filepath.Join(dir, "poppler", "pdftoppm")
		if _, err := os.Stat(bundledUnix); err == nil {
			return bundledUnix, nil
		}
	}
	return exec.LookPath("pdftoppm")
}
