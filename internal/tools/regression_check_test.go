package tools

import (
	"os"
	"testing"
)

func TestRegressionRealPDFs(t *testing.T) {
	// Point TERMINUS_REGRESSION_PDFS at a folder of your own PDFs to run this check.
	dir := os.Getenv("TERMINUS_REGRESSION_PDFS")
	if dir == "" {
		t.Skip("TERMINUS_REGRESSION_PDFS not set")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skip("real_pdfs regression folder not present")
	}
	sum, err := CheckPDFDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Total == 0 {
		t.Fatal("no PDFs in regression folder")
	}
	if sum.Failed > 0 {
		t.Fatalf("regression failures (%d/%d): %v", sum.Failed, sum.Total, sum.Failures)
	}
	t.Logf("regression: total=%d clean=%d warnings=%d", sum.Total, sum.Clean, sum.WithWarnings)
}
