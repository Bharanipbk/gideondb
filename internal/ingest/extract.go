// Package ingest turns uploaded documents into text chunks ready to embed:
// it extracts text from plain text, Markdown, HTML, PDF and DOCX, then
// splits it into overlapping chunks on paragraph, sentence and word
// boundaries.
package ingest

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"rsc.io/pdf"
)

// Format is a supported document format.
type Format string

const (
	FormatText     Format = "text"
	FormatMarkdown Format = "markdown"
	FormatHTML     Format = "html"
	FormatPDF      Format = "pdf"
	FormatDOCX     Format = "docx"
)

// ErrUnsupported reports a format that cannot be extracted.
var ErrUnsupported = errors.New("unsupported document format")

// MaxExtractedBytes bounds extracted text, so a small compressed file
// cannot expand without limit.
const MaxExtractedBytes = 16 << 20

// Document is extracted text and an optional title.
type Document struct {
	Format Format
	Title  string
	Text   string
}

// DetectFormat picks a format from the content type, then the file name,
// then the leading bytes.
func DetectFormat(contentType, filename string, data []byte) (Format, error) {
	if media, _, err := mime.ParseMediaType(contentType); err == nil {
		switch media {
		case "text/plain":
			return FormatText, nil
		case "text/markdown", "text/x-markdown":
			return FormatMarkdown, nil
		case "text/html", "application/xhtml+xml":
			return FormatHTML, nil
		case "application/pdf":
			return FormatPDF, nil
		case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
			return FormatDOCX, nil
		}
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".txt", ".text", ".csv", ".log":
		return FormatText, nil
	case ".md", ".markdown":
		return FormatMarkdown, nil
	case ".html", ".htm", ".xhtml":
		return FormatHTML, nil
	case ".pdf":
		return FormatPDF, nil
	case ".docx":
		return FormatDOCX, nil
	}
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return FormatPDF, nil
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return FormatDOCX, nil
	case utf8.Valid(data):
		head := strings.ToLower(string(data[:min(len(data), 512)]))
		if strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html") {
			return FormatHTML, nil
		}
		return FormatText, nil
	}
	return "", fmt.Errorf("%w: cannot tell the format of %q", ErrUnsupported, filename)
}

// Extract returns the document's text.
func Extract(format Format, data []byte) (Document, error) {
	var (
		doc Document
		err error
	)
	switch format {
	case FormatText:
		doc, err = Document{Text: string(data)}, validUTF8(data)
	case FormatMarkdown:
		doc, err = extractMarkdown(data)
	case FormatHTML:
		doc, err = extractHTML(data)
	case FormatPDF:
		doc, err = extractPDF(data)
	case FormatDOCX:
		doc, err = extractDOCX(data)
	default:
		return Document{}, fmt.Errorf("%w: %s", ErrUnsupported, format)
	}
	if err != nil {
		return Document{}, err
	}
	doc.Format = format
	doc.Text = normalizeWhitespace(doc.Text)
	if len(doc.Text) > MaxExtractedBytes {
		return Document{}, fmt.Errorf("extracted text exceeds %d bytes", MaxExtractedBytes)
	}
	return doc, nil
}

func validUTF8(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("text is not valid UTF-8")
	}
	return nil
}

var (
	markdownHeading = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+(.+?)\s*#*\s*$`)
	markdownLink    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	markdownFence   = regexp.MustCompile("(?m)^\\s*(```|~~~).*$")
	markdownMarks   = regexp.MustCompile("(\\*\\*|__|`)")
)

// extractMarkdown keeps the text, drops link targets and emphasis marks,
// and uses the first heading as the title.
func extractMarkdown(data []byte) (Document, error) {
	if err := validUTF8(data); err != nil {
		return Document{}, err
	}
	text := string(data)
	var title string
	if match := markdownHeading.FindStringSubmatch(text); match != nil {
		title = strings.TrimSpace(match[1])
	}
	text = markdownHeading.ReplaceAllString(text, "$1\n")
	text = markdownLink.ReplaceAllString(text, "$1")
	text = markdownFence.ReplaceAllString(text, "")
	text = markdownMarks.ReplaceAllString(text, "")
	return Document{Title: title, Text: text}, nil
}

var htmlBlocks = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "section": true, "article": true, "header": true, "footer": true,
	"blockquote": true, "pre": true, "table": true, "ul": true, "ol": true, "dd": true, "dt": true,
}

// extractHTML collects visible text, with paragraph breaks at block
// elements, and skips scripts, styles and other non-content elements.
func extractHTML(data []byte) (Document, error) {
	root, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return Document{}, err
	}
	var b strings.Builder
	var title string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template", "svg", "head":
				if n.Data == "head" {
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if c.Type == html.ElementNode && c.Data == "title" && c.FirstChild != nil {
							title = strings.TrimSpace(c.FirstChild.Data)
						}
					}
				}
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && htmlBlocks[n.Data] {
			b.WriteString("\n\n")
		}
	}
	walk(root)
	return Document{Title: title, Text: b.String()}, nil
}

// extractPDF walks each page's content stream and decodes text-showing
// operators through the page fonts' encodings. Word breaks come from
// explicit spaces, large TJ kerning gaps and horizontal moves; line breaks
// from vertical moves. Scanned PDFs (images only) yield no text; OCR is out
// of scope.
func extractPDF(data []byte) (doc Document, err error) {
	defer func() {
		// The PDF reader panics on some malformed files.
		if r := recover(); r != nil {
			doc, err = Document{}, fmt.Errorf("cannot read PDF: %v", r)
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Document{}, fmt.Errorf("cannot read PDF: %w", err)
	}
	var b strings.Builder
	for i := 1; i <= reader.NumPage(); i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		if err := pageText(page, &b); err != nil {
			return Document{}, err
		}
		b.WriteString("\n\n")
	}
	if title := reader.Trailer().Key("Info").Key("Title"); title.Kind() == pdf.String {
		doc.Title = strings.TrimSpace(title.Text())
	}
	doc.Text = b.String()
	if strings.TrimSpace(doc.Text) == "" {
		return Document{}, fmt.Errorf("PDF has no extractable text (scanned documents need OCR first)")
	}
	return doc, nil
}

// tjWordGap is the TJ adjustment (thousandths of an em, negative moves
// right) treated as a space between words.
const tjWordGap = -180

func pageText(page pdf.Page, b *strings.Builder) error {
	contents := page.V.Key("Contents")
	var streams []pdf.Value
	if contents.Kind() == pdf.Array {
		for i := 0; i < contents.Len(); i++ {
			streams = append(streams, contents.Index(i))
		}
	} else if contents.Kind() == pdf.Stream {
		streams = append(streams, contents)
	}
	encoders := map[string]pdf.TextEncoding{}
	var encoder pdf.TextEncoding
	needSpace := false
	write := func(text string) {
		if text == "" {
			return
		}
		if needSpace && b.Len() > 0 {
			last, _ := utf8.DecodeLastRuneInString(b.String()[max(0, b.Len()-4):])
			if last != ' ' && last != '\n' && !strings.HasPrefix(text, " ") {
				b.WriteByte(' ')
			}
		}
		needSpace = false
		b.WriteString(text)
	}
	decode := func(raw string) string {
		if encoder == nil {
			return raw
		}
		return encoder.Decode(raw)
	}
	newline := func() {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
		needSpace = false
	}
	for _, stream := range streams {
		pdf.Interpret(stream, func(stack *pdf.Stack, op string) {
			n := stack.Len()
			args := make([]pdf.Value, n)
			for i := n - 1; i >= 0; i-- {
				args[i] = stack.Pop()
			}
			switch op {
			case "Tf":
				if n >= 1 {
					name := args[0].Name()
					if _, ok := encoders[name]; !ok {
						encoders[name] = page.Font(name).Encoder()
					}
					encoder = encoders[name]
				}
			case "Tj":
				if n >= 1 {
					write(decode(args[n-1].RawString()))
				}
			case "'", "\"":
				newline()
				if n >= 1 {
					write(decode(args[n-1].RawString()))
				}
			case "TJ":
				if n >= 1 && args[n-1].Kind() == pdf.Array {
					array := args[n-1]
					for i := 0; i < array.Len(); i++ {
						item := array.Index(i)
						switch item.Kind() {
						case pdf.String:
							write(decode(item.RawString()))
						case pdf.Integer, pdf.Real:
							if item.Float64() <= tjWordGap {
								needSpace = true
							}
						}
					}
				}
			case "Td", "TD":
				if n >= 2 {
					if args[1].Float64() != 0 {
						newline()
					} else if args[0].Float64() > 0 {
						needSpace = true
					}
				}
			case "T*":
				newline()
			case "Tm":
				newline()
			case "ET":
				needSpace = true
			}
			if b.Len() > MaxExtractedBytes {
				panic(fmt.Sprintf("extracted text exceeds %d bytes", MaxExtractedBytes))
			}
		})
	}
	return nil
}

// extractDOCX reads word/document.xml paragraphs and the core title.
func extractDOCX(data []byte) (Document, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Document{}, fmt.Errorf("cannot read DOCX: %w", err)
	}
	var doc Document
	for _, file := range archive.File {
		switch file.Name {
		case "word/document.xml":
			text, err := docxText(file)
			if err != nil {
				return Document{}, err
			}
			doc.Text = text
		case "docProps/core.xml":
			doc.Title, _ = docxTitle(file)
		}
	}
	if doc.Text == "" {
		return Document{}, fmt.Errorf("cannot read DOCX: word/document.xml has no text")
	}
	return doc, nil
}

func docxText(file *zip.File) (string, error) {
	reader, err := file.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, MaxExtractedBytes*4))
	var b strings.Builder
	inText := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return b.String(), nil
		}
		if err != nil {
			return "", fmt.Errorf("cannot read DOCX: %w", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteString("\n\n")
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
		if b.Len() > MaxExtractedBytes {
			return "", fmt.Errorf("extracted text exceeds %d bytes", MaxExtractedBytes)
		}
	}
}

func docxTitle(file *zip.File) (string, error) {
	reader, err := file.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	var core struct {
		Title string `xml:"title"`
	}
	if err := xml.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(&core); err != nil {
		return "", err
	}
	return strings.TrimSpace(core.Title), nil
}

var (
	horizontalSpace = regexp.MustCompile(`[ \t\f\v\x{00a0}]+`)
	manyNewlines    = regexp.MustCompile(`\n{3,}`)
)

// normalizeWhitespace collapses runs of spaces, trims lines and keeps at
// most one blank line between paragraphs.
func normalizeWhitespace(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = horizontalSpace.ReplaceAllString(text, " ")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	text = strings.Join(lines, "\n")
	return strings.TrimSpace(manyNewlines.ReplaceAllString(text, "\n\n"))
}
