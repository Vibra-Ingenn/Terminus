# Changes in this copy of github.com/ledongthuc/pdf

Copied from `github.com/ledongthuc/pdf v0.0.0-20260907135840-6c8c28e0e8a0` (BSD licence, see
`LICENSE`) on 2026-10-27, because these fixes are inside the library. Terminus uses it through the
`replace` line in its `go.mod`. The `examples` and `pdfpasswd` folders were left out.

1. **40-bit RC4 locked PDFs** (`read.go`, `cryptKey`): the per-object key is now the first n+5
   bytes of the MD5 hash (at most 16), as the PDF standard says (Algorithm 1, step d). The
   original always used 16 bytes, so every stream in a 40-bit locked file came out scrambled
   (`zlib: invalid header`). 128-bit RC4 and AES-128 were right by luck.
2. **AES-256 locked PDFs** (`encrypt_v5.go`, new): Standard security handler V=5, R=5 and R=6
   (ISO 32000-2, Algorithms 2.A and 2.B), with the user or the owner password. The original
   rejected these as `malformed PDF: 256-bit encryption key`.
3. **`Value.RawBytes`** (`read.go`, new): a stream's bytes after decryption but before its
   filter, for JPEG/CCITT images. Terminus used to read these straight from the file, which
   gave scrambled images for locked PDFs.
4. **Text inside forms** (`page.go`, `GetPlainText` and `Content`): `Do` on a Form XObject now
   reads the form's text, with the form's own fonts and `/Matrix`, nested up to 10 deep, with a
   loop guard. Many PDFs (re-saved, stamped, "printed to PDF") keep all their text in forms, and
   the original returned nothing for them.
5. **`"` operator** (`page.go`, `GetPlainText`): it now shows its text. The original fell through
   into the `'` case and panicked on its three arguments.

Tests: `go test .` in this folder (one key-length test updated to the standard). Terminus's own
tests for these: `internal/tools/locked_and_form_test.go`, fixtures from
`scripts/gen_locked_and_form_test_pdfs.py`.
