package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/api/rest"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/pkg/client"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func inMemoryHTTPClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})}
}

func TestClientLifecycleAndTypedErrors(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const apiKey = "0123456789abcdef"
	httpClient := inMemoryHTTPClient(rest.NewWithOptions(db, nil, rest.Options{APIKey: apiKey, NodeID: "11111111111111111111111111111111", ClusterID: "22222222222222222222222222222222", AdvertiseAddress: "gideondb.test:6333"}).Handler())
	sdk, err := client.New("http://gideondb.test", client.Options{APIKey: apiKey, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sdk.Health(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := sdk.CreateCollection(ctx, client.CollectionConfig{Name: "docs", Dimension: 2, Metric: "dot", ShardCount: 2})
	if err != nil || created.Name != "docs" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	stored, err := sdk.Upsert(ctx, "docs", client.Record{ID: "one", Namespace: "tenant-a", Vector: []float32{1, 0}, Metadata: map[string]any{"kind": "guide"}})
	if err != nil || stored.Version == 0 {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if _, err := sdk.BatchUpsert(ctx, "docs", []client.Record{{ID: "two", Vector: []float32{0, 1}}}); err != nil {
		t.Fatal(err)
	}
	page, err := sdk.Scroll(ctx, "docs", client.ScrollOptions{Limit: 1})
	if err != nil || len(page.Records) != 1 || page.NextCursor == "" || len(page.Records[0].Vector) != 0 || page.VectorsIncluded {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	next, err := sdk.Scroll(ctx, "docs", client.ScrollOptions{Limit: 2, Cursor: page.NextCursor, IncludeVector: true})
	if err != nil || len(next.Records) != 1 || len(next.Records[0].Vector) != 2 || !next.VectorsIncluded {
		t.Fatalf("next=%#v err=%v", next, err)
	}
	clusterPage, err := sdk.DistributedScroll(ctx, "docs", client.ScrollOptions{Limit: 2})
	if err != nil || len(clusterPage.Records) != 2 || clusterPage.MetadataEpoch == 0 {
		t.Fatalf("cluster page=%#v err=%v", clusterPage, err)
	}
	got, err := sdk.Get(ctx, "docs", "tenant-a", "one")
	if err != nil || got.ID != "one" || got.Namespace != "tenant-a" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	results, err := sdk.Search(ctx, "docs", client.SearchOptions{Vector: []float32{1, 0}, TopK: 1, Namespace: "tenant-a", Filter: map[string]any{"kind": "guide"}})
	if err != nil || len(results) != 1 || results[0].ID != "one" {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	description, err := sdk.DescribeCollection(ctx, "docs")
	if err != nil || description.VectorCount != 2 {
		t.Fatalf("description=%#v err=%v", description, err)
	}
	collections, err := sdk.ListCollections(ctx)
	if err != nil || len(collections) != 1 {
		t.Fatalf("collections=%#v err=%v", collections, err)
	}
	if err := sdk.Delete(ctx, "docs", "tenant-a", "one"); err != nil {
		t.Fatal(err)
	}
	_, err = sdk.Get(ctx, "docs", "tenant-a", "one")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 || apiErr.Code != "not_found" {
		t.Fatalf("error=%#v", err)
	}
	if err := sdk.DeleteCollection(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
}

func TestClientScrollRejectsInvalidLimit(t *testing.T) {
	sdk, err := client.New("http://gideondb.test", client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.Scroll(context.Background(), "docs", client.ScrollOptions{Limit: 201}); err == nil {
		t.Fatal("accepted oversized scroll page")
	}
	if _, err := sdk.DistributedScroll(context.Background(), "docs", client.ScrollOptions{Limit: 201}); err == nil {
		t.Fatal("accepted oversized distributed scroll page")
	}
}

func TestClientDistributedEndpointsAndValidation(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := client.New("ftp://invalid", client.Options{}); err == nil {
		t.Fatal("accepted non-HTTP base URL")
	}
	httpClient := inMemoryHTTPClient(rest.NewWithOptions(db, nil, rest.Options{
		NodeID: "11111111111111111111111111111111", ClusterID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AdvertiseAddress: "node-a:6333", MetadataEpoch: 1,
	}).Handler())
	sdk, err := client.New("http://gideondb.test", client.Options{HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := sdk.CreateCollection(ctx, client.CollectionConfig{Name: "distributed", Dimension: 2, Metric: "dot", ShardCount: 2}); err != nil {
		t.Fatal(err)
	}
	write, err := sdk.DistributedBatchUpsert(ctx, "distributed", []client.Record{{ID: "one", Vector: []float32{1, 0}}}, "quorum")
	if err != nil || write.Partial || len(write.Outcomes) != 1 {
		t.Fatalf("write=%#v err=%v", write, err)
	}
	search, err := sdk.DistributedSearch(ctx, "distributed", client.SearchOptions{Vector: []float32{1, 0}, TopK: 1})
	if err != nil || search.Partial || len(search.Results) != 1 || search.Results[0].ID != "one" {
		t.Fatalf("search=%#v err=%v", search, err)
	}
}
