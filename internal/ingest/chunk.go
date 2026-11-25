package ingest

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Chunk is one piece of a document and its rune range in the text.
type Chunk struct {
	Text       string
	Start, End int // rune offsets into the extracted text
}

// ChunkOptions sizes chunks in runes.
type ChunkOptions struct {
	Size    int // target maximum length; default 1000
	Overlap int // runes repeated from the previous chunk; default 150
}

const (
	MinChunkSize = 100
	MaxChunkSize = 8000
	MaxChunks    = 5000
)

func (o ChunkOptions) normalized() (ChunkOptions, error) {
	if o.Size == 0 {
		o.Size = 1000
	}
	if o.Overlap == 0 && o.Size >= 300 {
		o.Overlap = 150
	}
	if o.Size < MinChunkSize || o.Size > MaxChunkSize {
		return o, fmt.Errorf("chunk_size must be between %d and %d", MinChunkSize, MaxChunkSize)
	}
	if o.Overlap < 0 || o.Overlap > o.Size/2 {
		return o, fmt.Errorf("chunk_overlap must be between 0 and half of chunk_size")
	}
	return o, nil
}

// Split cuts text into chunks of at most Size runes. A cut is placed at
// the last paragraph break in the window, else the last sentence end, else
// the last space, else at Size. Each chunk after the first starts Overlap
// runes before the previous cut, moved forward to a word start.
func Split(text string, options ChunkOptions) ([]Chunk, error) {
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
	runes := []rune(text)
	var chunks []Chunk
	for start := 0; start < len(runes); {
		for start < len(runes) && unicode.IsSpace(runes[start]) {
			start++
		}
		if start >= len(runes) {
			break
		}
		end := min(start+options.Size, len(runes))
		if end < len(runes) {
			end = cutPoint(runes, start, end)
		}
		piece := strings.TrimSpace(string(runes[start:end]))
		if piece != "" {
			chunks = append(chunks, Chunk{Text: piece, Start: start, End: end})
			if len(chunks) > MaxChunks {
				return nil, fmt.Errorf("document splits into more than %d chunks; raise chunk_size", MaxChunks)
			}
		}
		if end >= len(runes) {
			break
		}
		next := end - options.Overlap
		if next <= start {
			next = end
		}
		// Start the overlap at a word boundary.
		for next < end && next > 0 && !unicode.IsSpace(runes[next-1]) {
			next++
		}
		start = next
	}
	return chunks, nil
}

// cutPoint returns the best boundary in (start, end], preferring a cut in
// the second half of the window so chunks stay near the target size.
func cutPoint(runes []rune, start, end int) int {
	floor := start + (end-start)/2
	for i := end - 1; i > floor; i-- {
		if runes[i] == '\n' && runes[i-1] == '\n' {
			return i + 1
		}
	}
	for i := end - 1; i > floor; i-- {
		if (runes[i-1] == '.' || runes[i-1] == '!' || runes[i-1] == '?' || runes[i-1] == '。') && unicode.IsSpace(runes[i]) {
			return i
		}
	}
	for i := end - 1; i > floor; i-- {
		if unicode.IsSpace(runes[i]) {
			return i
		}
	}
	return end
}

// RuneLen is the length used by Split.
func RuneLen(text string) int { return utf8.RuneCountInString(text) }
