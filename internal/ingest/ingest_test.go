package ingest

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestSplitRespectsSizeOverlapAndBoundaries(t *testing.T) {
	var b strings.Builder
	for i := range 40 {
		fmt.Fprintf(&b, "Sentence number %d is here. ", i)
		if i%10 == 9 {
			b.WriteString("\n\n")
		}
	}
	text := normalizeWhitespace(b.String())
	chunks, err := Split(text, ChunkOptions{Size: 300, Overlap: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 4 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for i, chunk := range chunks {
		if RuneLen(chunk.Text) > 300 {
			t.Fatalf("chunk %d has %d runes", i, RuneLen(chunk.Text))
		}
		if i < len(chunks)-1 && !strings.HasSuffix(chunk.Text, ".") {
			t.Fatalf("chunk %d does not end at a sentence: %q", i, chunk.Text)
		}
		if i > 0 && chunk.Start >= chunks[i-1].End {
			t.Fatalf("chunk %d does not overlap the previous one", i)
		}
		if strings.HasPrefix(chunk.Text, "umber") || strings.HasPrefix(chunk.Text, "entence") {
			t.Fatalf("chunk %d starts mid-word: %q", i, chunk.Text)
		}
	}
	if !strings.Contains(chunks[len(chunks)-1].Text, "number 39") {
		t.Fatal("last sentence lost")
	}
	// A single unbroken token is still cut at the size limit.
	long, err := Split(strings.Repeat("x", 1000), ChunkOptions{Size: 400, Overlap: 0})
	if err != nil || len(long) != 3 {
		t.Fatalf("long token chunks = %d, %v", len(long), err)
	}
	if _, err := Split("x", ChunkOptions{Size: 50}); err == nil {
		t.Fatal("tiny chunk size accepted")
	}
	if _, err := Split("x", ChunkOptions{Size: 500, Overlap: 400}); err == nil {
		t.Fatal("overlap above half accepted")
	}
}

func TestExtractHTMLAndMarkdown(t *testing.T) {
	doc, err := Extract(FormatHTML, []byte(`<!doctype html><html><head><title>Guide</title><style>p{}</style></head>
		<body><h1>Install</h1><p>Run   the <b>server</b>.</p><script>alert(1)</script><ul><li>one</li><li>two</li></ul></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Guide" || doc.Text != "Install\n\nRun the server.\n\none\n\ntwo" {
		t.Fatalf("html = %q / %q", doc.Title, doc.Text)
	}
	doc, err = Extract(FormatMarkdown, []byte("# Vector Search\n\nUse **HNSW** with [the guide](http://x).\n\n```go\ncode()\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Vector Search" || !strings.Contains(doc.Text, "Use HNSW with the guide.") || strings.Contains(doc.Text, "http") {
		t.Fatalf("markdown = %q / %q", doc.Title, doc.Text)
	}
	if _, err := Extract(FormatText, []byte{0xff, 0xfe}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestExtractDOCX(t *testing.T) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	write := func(name, body string) {
		w, _ := archive.Create(name)
		_, _ = w.Write([]byte(body))
	}
	write("word/document.xml", `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
		<w:p><w:r><w:t>Quarterly</w:t></w:r><w:r><w:t xml:space="preserve"> report</w:t></w:r></w:p>
		<w:p><w:r><w:t>Revenue grew.</w:t></w:r></w:p></w:body></w:document>`)
	write("docProps/core.xml", `<?xml version="1.0"?><cp:coreProperties xmlns:cp="x" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Q3</dc:title></cp:coreProperties>`)
	_ = archive.Close()
	format, err := DetectFormat("", "report.docx", buffer.Bytes())
	if err != nil || format != FormatDOCX {
		t.Fatalf("format = %v %v", format, err)
	}
	doc, err := Extract(format, buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Q3" || doc.Text != "Quarterly report\n\nRevenue grew." {
		t.Fatalf("docx = %q / %q", doc.Title, doc.Text)
	}
}

// minimalPDF builds a valid one-page PDF with two lines of text.
func minimalPDF(lines ...string) []byte {
	var content strings.Builder
	content.WriteString("BT /F1 12 Tf 72 720 Td ")
	for i, line := range lines {
		if i > 0 {
			content.WriteString("0 -30 Td ")
		}
		if strings.HasPrefix(line, "[") {
			fmt.Fprintf(&content, "%s TJ ", line) // raw TJ array
		} else {
			fmt.Fprintf(&content, "(%s) Tj ", line)
		}
	}
	content.WriteString("ET")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", content.Len(), content.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Title (Handbook) >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Info 6 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

func TestExtractPDF(t *testing.T) {
	data := minimalPDF("Hello vector world", "Second line here")
	format, err := DetectFormat("application/octet-stream", "upload", data)
	if err != nil || format != FormatPDF {
		t.Fatalf("format = %v %v", format, err)
	}
	doc, err := Extract(format, data)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Handbook" || !strings.Contains(doc.Text, "Hello vector world") || !strings.Contains(doc.Text, "Second line here") {
		t.Fatalf("pdf = %q / %q", doc.Title, doc.Text)
	}
	kerned, err := Extract(FormatPDF, minimalPDF("[(Kern) -30 (ing) -250 (works)]"))
	if err != nil || !strings.Contains(kerned.Text, "Kerning works") {
		t.Fatalf("TJ kerning = %q, %v", kerned.Text, err)
	}
	if _, err := Extract(FormatPDF, []byte("%PDF-1.4 garbage")); err == nil {
		t.Fatal("corrupt PDF accepted")
	}
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		contentType, name string
		data              string
		want              Format
	}{
		{"text/markdown; charset=utf-8", "", "x", FormatMarkdown},
		{"", "notes.md", "x", FormatMarkdown},
		{"", "page", "<!DOCTYPE html><p>x", FormatHTML},
		{"", "page", "plain words", FormatText},
	}
	for _, c := range cases {
		if got, err := DetectFormat(c.contentType, c.name, []byte(c.data)); err != nil || got != c.want {
			t.Errorf("%q %q = %v %v, want %v", c.contentType, c.name, got, err, c.want)
		}
	}
	if _, err := DetectFormat("", "blob", []byte{0xff, 0x00, 0xfe}); err == nil {
		t.Error("binary blob accepted")
	}
}
