package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestSearchShapeUnit(t *testing.T) {
	results := make([]core.SearchResult, 0, 9)
	for i, author := range []any{"a", "b", "a", nil, "c", "b", "a", "d", float64(1)} {
		metadata := map[string]any{}
		if author != nil {
			metadata["author"] = author
		}
		results = append(results, core.SearchResult{ID: fmt.Sprint(i), Score: float32(10 - i), Metadata: metadata})
	}
	request := searchRequest{TopK: 2, Offset: 1, GroupBy: "author", GroupSize: 2}
	shape, err := request.takeShape()
	if err != nil || request.TopK != (1+2)*2*groupOversample || request.GroupBy != "" || request.Offset != 0 {
		t.Fatalf("takeShape = %+v %v, request = %+v", shape, err, request)
	}
	key, value := shape.apply(results)
	groups := value.([]searchGroup)
	// Group order by best hit: a(0), b(1), c(4), d(7), 1(8); offset 1 skips a.
	if key != "groups" || len(groups) != 2 || groups[0].Value != "b" || groups[1].Value != "c" {
		t.Fatalf("groups = %+v", groups)
	}
	if len(groups[0].Hits) != 2 || groups[0].Hits[0].ID != "1" || groups[0].Hits[1].ID != "5" {
		t.Fatalf("b hits = %+v", groups[0].Hits)
	}
	page := searchRequest{TopK: 3, Offset: 2}
	pageShape, _ := page.takeShape()
	if page.TopK != 5 {
		t.Fatalf("fetch = %d", page.TopK)
	}
	if _, value := pageShape.apply(results); fmt.Sprint(ids(value.([]core.SearchResult))) != "[2 3 4]" {
		t.Fatalf("page = %v", value)
	}
	if _, value := (searchShape{topK: 5, offset: 20}).apply(results); len(value.([]core.SearchResult)) != 0 {
		t.Fatalf("offset past end = %v", value)
	}
	for _, bad := range []searchRequest{
		{TopK: 1, Offset: -1},
		{TopK: 1, GroupSize: 2},
		{TopK: 9_999, Offset: 2},
		{TopK: 1, GroupBy: "x", GroupSize: maxGroupSize + 1},
	} {
		if _, err := bad.takeShape(); err == nil {
			t.Fatalf("%+v should be rejected", bad)
		}
	}
}

func ids(results []core.SearchResult) []string {
	out := make([]string, len(results))
	for i, result := range results {
		out[i] = result.ID
	}
	return out
}

func TestSearchGroupByAndOffsetOverHTTP(t *testing.T) {
	h := embeddingServer(t)
	send(t, h, http.MethodPost, "/v1/collections", `{"name":"posts","dimension":2,"metric":"l2","shard_count":3}`)
	records := []map[string]any{}
	for i := 0; i < 12; i++ {
		records = append(records, map[string]any{"id": fmt.Sprint(i), "vector": []float32{float32(i), 0}, "metadata": map[string]any{"author": fmt.Sprint("u", i%3)}})
	}
	body, _ := json.Marshal(map[string]any{"records": records})
	if code, reply := send(t, h, http.MethodPost, "/v1/collections/posts/vectors/batch", string(body)); code != http.StatusOK {
		t.Fatalf("batch = %d %v", code, reply)
	}
	_, page := send(t, h, http.MethodPost, "/v1/collections/posts/search", `{"vector":[0,0],"top_k":3,"offset":3}`)
	got := []string{}
	for _, item := range page["results"].([]any) {
		got = append(got, item.(map[string]any)["id"].(string))
	}
	if fmt.Sprint(got) != "[3 4 5]" {
		t.Fatalf("offset page = %v", page)
	}
	_, grouped := send(t, h, http.MethodPost, "/v1/collections/posts/search", `{"vector":[0,0],"top_k":2,"group_by":"author","group_size":2}`)
	groups := grouped["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("groups = %v", grouped)
	}
	first := groups[0].(map[string]any)
	hits := first["hits"].([]any)
	if first["value"] != "u0" || len(hits) != 2 || hits[0].(map[string]any)["id"] != "0" || hits[1].(map[string]any)["id"] != "3" {
		t.Fatalf("first group = %v", first)
	}
	if code, _ := send(t, h, http.MethodPost, "/v1/collections/posts/search", `{"vector":[0,0],"top_k":1,"offset":-1}`); code != http.StatusBadRequest {
		t.Fatalf("negative offset status = %d", code)
	}
	_, geo := send(t, h, http.MethodPost, "/v1/collections/posts/search", `{"vector":[0,0],"top_k":5,"filter":{"author":{"$text":"U1"}}}`)
	if n := len(geo["results"].([]any)); n != 4 {
		t.Fatalf("$text filter over HTTP returned %d results: %v", n, geo)
	}
}
