package tools

import "testing"

func TestDocxKeepsTableRows(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		`<w:p><w:r><w:t>Action Items</w:t></w:r></w:p>` +
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Owner</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Task</w:t></w:r></w:p></w:tc></w:tr>` +
		`<w:tr><w:tc><w:p><w:r><w:t>Dana</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Send </w:t></w:r><w:r><w:t>invoice</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
		`<w:p><w:r><w:t>End.</w:t></w:r></w:p></w:body></w:document>`)
	got := docxPlainText(raw)
	want := "Action Items\nOwner\tTask\nDana\tSend invoice\nEnd."
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
