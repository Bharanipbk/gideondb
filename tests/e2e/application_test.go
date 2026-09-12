//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	testAPIKey            = "gideondb-e2e-api-key-0123456789"
	testDashboardPassword = "gideondb-e2e-dashboard-password"
)

type application struct {
	baseURL string
	client  *http.Client
	command *exec.Cmd
	logFile *os.File
}

func TestApplicationLifecycleAndRecovery(t *testing.T) {
	root := repositoryRoot(t)
	working := t.TempDir()
	binary := filepath.Join(working, "gideondb")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/gideondb")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build application: %v\n%s", err, output)
	}

	dataPath := filepath.Join(working, "data")
	keyPath := filepath.Join(working, "api-key")
	if err := os.WriteFile(keyPath, []byte(testAPIKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	app := startApplication(t, binary, dataPath, keyPath, filepath.Join(working, "first-run.log"))
	t.Cleanup(func() { app.stop(t) })

	assertStatus(t, app.request(t, http.MethodGet, "/v1/health", nil, false), http.StatusOK)
	assertStatus(t, app.request(t, http.MethodGet, "/v1/collections", nil, false), http.StatusUnauthorized)
	assertStatus(t, app.request(t, http.MethodGet, "/dashboard/", nil, false), http.StatusOK)
	assertStatus(t, app.request(t, http.MethodGet, "/docs/", nil, false), http.StatusOK)

	app.requestJSON(t, http.MethodPost, "/v1/collections", map[string]any{
		"name": "e2e-documents", "dimension": 3, "metric": "cosine", "shard_count": 2,
	}, true, http.StatusCreated)
	for _, record := range []map[string]any{
		{"id": "doc-a", "namespace": "tenant-a", "vector": []float64{1, 0, 0}, "metadata": map[string]any{"kind": "guide", "active": true}},
		{"id": "doc-b", "namespace": "tenant-a", "vector": []float64{0.8, 0.2, 0}, "metadata": map[string]any{"kind": "guide", "active": true}},
		{"id": "doc-c", "namespace": "tenant-b", "vector": []float64{0, 1, 0}, "metadata": map[string]any{"kind": "note", "active": false}},
	} {
		app.requestJSON(t, http.MethodPost, "/v1/collections/e2e-documents/vectors", record, true, http.StatusOK)
	}

	search := app.requestJSON(t, http.MethodPost, "/v1/collections/e2e-documents/search", map[string]any{
		"vector": []float64{1, 0, 0}, "top_k": 2, "namespace": "tenant-a", "filter": map[string]any{"kind": "guide"},
	}, true, http.StatusOK)
	results, _ := search["results"].([]any)
	if len(results) != 2 || results[0].(map[string]any)["id"] != "doc-a" {
		t.Fatalf("unexpected ranked search results: %#v", search)
	}

	redacted := app.requestJSON(t, http.MethodGet, "/v1/collections/e2e-documents/vectors?limit=2", nil, true, http.StatusOK)
	redactedRecords, _ := redacted["records"].([]any)
	if len(redactedRecords) != 2 || redacted["vectors_included"] != false {
		t.Fatalf("unexpected redacted page: %#v", redacted)
	}
	if _, exists := redactedRecords[0].(map[string]any)["vector"]; exists {
		t.Fatalf("default scroll leaked vector: %#v", redactedRecords[0])
	}
	if cursor, _ := redacted["next_cursor"].(string); cursor == "" {
		t.Fatalf("expected pagination cursor: %#v", redacted)
	}

	visible := app.requestJSON(t, http.MethodGet, "/v1/collections/e2e-documents/vectors?namespace=tenant-a&include_vector=true", nil, true, http.StatusOK)
	visibleRecords, _ := visible["records"].([]any)
	if len(visibleRecords) != 2 || visible["vectors_included"] != true {
		t.Fatalf("unexpected vector-inclusive page: %#v", visible)
	}
	if _, exists := visibleRecords[0].(map[string]any)["vector"]; !exists {
		t.Fatalf("explicit vector scroll omitted vector: %#v", visibleRecords[0])
	}

	metrics := readBody(t, app.request(t, http.MethodGet, "/metrics", nil, true))
	if !strings.Contains(metrics, `gideondb_vectors{collection="e2e-documents"} 3`) {
		t.Fatalf("metrics did not expose persisted vector count:\n%s", metrics)
	}

	session := app.requestJSON(t, http.MethodPost, "/v1/dashboard/session", map[string]any{
		"username": "admin", "password": testDashboardPassword,
	}, false, http.StatusOK)
	if authenticated, _ := session["authenticated"].(bool); !authenticated {
		t.Fatalf("dashboard login did not authenticate: %#v", session)
	}

	app.stop(t)
	app = startApplication(t, binary, dataPath, keyPath, filepath.Join(working, "second-run.log"))

	described := app.requestJSON(t, http.MethodGet, "/v1/collections/e2e-documents", nil, true, http.StatusOK)
	if count, ok := described["vector_count"].(float64); !ok || count != 3 {
		t.Fatalf("restart did not recover all records: %#v", described)
	}
	recovered := app.requestJSON(t, http.MethodGet, "/v1/collections/e2e-documents/vectors/doc-a?namespace=tenant-a", nil, true, http.StatusOK)
	if recovered["id"] != "doc-a" {
		t.Fatalf("restart returned unexpected record: %#v", recovered)
	}

	assertStatus(t, app.request(t, http.MethodDelete, "/v1/collections/e2e-documents/vectors/doc-c?namespace=tenant-b", nil, true), http.StatusNoContent)
	assertStatus(t, app.request(t, http.MethodGet, "/v1/collections/e2e-documents/vectors/doc-c?namespace=tenant-b", nil, true), http.StatusNotFound)
	assertStatus(t, app.request(t, http.MethodDelete, "/v1/collections/e2e-documents", nil, true), http.StatusNoContent)
	assertStatus(t, app.request(t, http.MethodGet, "/v1/collections/e2e-documents", nil, true), http.StatusNotFound)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func startApplication(t *testing.T, binary, dataPath, keyPath, logPath string) *application {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary,
		"-http-address", address,
		"-grpc-address", "",
		"-data-path", dataPath,
		"-api-key-file", keyPath,
		"-wal-sync", "always",
		"-checkpoint-every", "2",
	)
	command.Env = append(os.Environ(),
		"GIDEONDB_DASHBOARD_USERNAME=admin",
		"GIDEONDB_DASHBOARD_PASSWORD="+testDashboardPassword,
	)
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	app := &application{baseURL: "http://" + address, client: &http.Client{Timeout: 5 * time.Second, Jar: jar}, command: command, logFile: logFile}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response, requestErr := app.client.Get(app.baseURL + "/v1/health")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return app
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	app.stop(t)
	logData, _ := os.ReadFile(logPath)
	t.Fatalf("application did not become healthy:\n%s", logData)
	return nil
}

func (app *application) stop(t *testing.T) {
	t.Helper()
	if app.command == nil || app.command.Process == nil {
		return
	}
	_ = app.command.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- app.command.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = app.command.Process.Kill()
		<-done
	}
	_ = app.logFile.Close()
	app.command = nil
}

func (app *application) requestJSON(t *testing.T, method, path string, body any, authenticated bool, expected int) map[string]any {
	t.Helper()
	response := app.request(t, method, path, body, authenticated)
	defer response.Body.Close()
	if response.StatusCode != expected {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s: got HTTP %d, want %d; body=%s", method, path, response.StatusCode, expected, payload)
	}
	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return decoded
}

func (app *application) request(t *testing.T, method, path string, body any, authenticated bool) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, app.baseURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+testAPIKey)
	}
	response, err := app.client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return response
}

func assertStatus(t *testing.T, response *http.Response, expected int) {
	t.Helper()
	if response.StatusCode == expected {
		_ = response.Body.Close()
		return
	}
	body := readBody(t, response)
	t.Fatalf("unexpected HTTP status: got %d, want %d; body=%s", response.StatusCode, expected, body)
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
