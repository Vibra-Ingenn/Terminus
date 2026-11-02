module terminus

go 1.27.0

require (
	github.com/go-pdf/fpdf v0.9.0
	github.com/ledongthuc/pdf v0.0.0-20260907135840-6c8c28e0e8a0
	golang.org/x/crypto v0.58.0
	golang.org/x/image v0.32.0
	modernc.org/sqlite v1.60.1
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.49.0 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// Fixed copy of the PDF reader (see third_party/pdf/CHANGES.md).
replace github.com/ledongthuc/pdf => ./third_party/pdf
