package rest

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func uploadDocument(t *testing.T, handler http.Handler, collection string, fields map[string]string, filename, contentType string, data []byte) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, value := range fields {
		_ = form.WriteField(key, value)
	}
	if filename != "" {
		header := map[string][]string{"Content-Disposition": {`form-data; name="file"; filename="` + filename + `"`}}
		if contentType != "" {
			header["Content-Type"] = []string{contentType}
		}
		part, _ := form.CreatePart(header)
		_, _ = part.Write(data)
	}
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/collections/"+collection+"/documents", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var decoded map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &decoded)
	return response.Code, decoded
}

func TestDocumentIngestionChunksEmbedsAndReplaces(t *testing.T) {
	handler := embeddingServer(t)
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "kb", "dimension": 4, "metric": "cosine", "shard_count": 2, "vectorizer": "keywords", "text_analyzer": "english"}, http.StatusCreated)
	requestJSON(t, handler, http.MethodPost, "/v1/collections", map[string]any{"name": "plain", "dimension": 4, "metric": "cosine", "shard_count": 1}, http.StatusCreated)

	var markdown strings.Builder
	markdown.WriteString("# Language Guide\n\n")
	for i := range 30 {
		markdown.WriteString("Go is a compiled language with goroutines and channels. ")
		if i%5 == 4 {
			markdown.WriteString("\n\nPython is popular for data work and scripting. ")
		}
	}
	code, result := uploadDocument(t, handler, "kb", map[string]string{"id": "guide", "metadata": `{"team":"docs"}`, "chunk_size": "400", "chunk_overlap": "80"}, "guide.md", "", []byte(markdown.String()))
	if code != http.StatusOK {
		t.Fatalf("upload = %d %v", code, result)
	}
	chunks := int(result["chunks"].(float64))
	if chunks < 4 || result["title"] != "Language Guide" || result["format"] != "markdown" {
		t.Fatalf("result = %v", result)
	}
	// Chunks are searchable by vector (embedded server-side) and BM25, and carry metadata.
	body := string(requestJSON(t, handler, http.MethodPost, "/v1/collections/kb/search", map[string]any{
		"text": "python scripting", "top_k": 3, "filter": map[string]any{"document_id": "guide"},
		"highlight": map[string]any{"pre_tag": "[", "post_tag": "]"},
	}, http.StatusOK))
	for _, want := range []string{`"document_id":"guide"`, `"team":"docs"`, `"title":"Language Guide"`, `"source":"guide.md"`, `[Python]`, `"id":"guide#000`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	// Re-uploading a shorter version removes the extra chunks.
	code, result = uploadDocument(t, handler, "kb", map[string]string{"id": "guide"}, "guide.txt", "text/plain", []byte("Rust is memory safe."))
	if code != http.StatusOK || result["chunks"].(float64) != 1 || int(result["replaced_chunks_removed"].(float64)) != chunks-1 {
		t.Fatalf("replace = %d %v", code, result)
	}
	body = string(requestJSON(t, handler, http.MethodPost, "/v1/collections/kb/search", map[string]any{
		"vector": []float32{1, 0, 0, 0}, "top_k": 50, "filter": map[string]any{"document_id": "guide"}, "facets": []map[string]any{{"field": "document_id"}},
	}, http.StatusOK))
	if strings.Contains(body, `"id":"guide#00001"`) || !strings.Contains(body, `"facets":{"document_id":[{"value":"guide","count":1}]}`) {
		t.Fatalf("stale chunks remain: %s", body)
	}
	// HTML is extracted; the default id is the file name.
	code, result = uploadDocument(t, handler, "kb", nil, "release-notes.html", "text/html", []byte(`<html><head><title>Notes</title></head><body><p>Go 1.26 ships.</p><script>x()</script></body></html>`))
	if code != http.StatusOK || result["document_id"] != "release-notes" || result["title"] != "Notes" {
		t.Fatalf("html = %d %v", code, result)
	}
	// Errors.
	if code, _ := uploadDocument(t, handler, "plain", nil, "a.txt", "text/plain", []byte("x")); code != http.StatusBadRequest {
		t.Fatalf("no vectorizer = %d", code)
	}
	if code, _ := uploadDocument(t, handler, "kb", nil, "", "", nil); code != http.StatusBadRequest {
		t.Fatalf("missing file = %d", code)
	}
	if code, _ := uploadDocument(t, handler, "kb", nil, "blob.bin", "application/octet-stream", []byte{0xff, 0, 0xfe, 1}); code != http.StatusUnsupportedMediaType {
		t.Fatalf("binary = %d", code)
	}
	if code, _ := uploadDocument(t, handler, "kb", nil, "bad.pdf", "application/pdf", []byte("%PDF-1.4 broken")); code != http.StatusUnprocessableEntity {
		t.Fatalf("broken pdf = %d", code)
	}
	if code, _ := uploadDocument(t, handler, "kb", map[string]string{"id": "../etc"}, "a.txt", "text/plain", []byte("x")); code != http.StatusBadRequest {
		t.Fatalf("bad id = %d", code)
	}
}
