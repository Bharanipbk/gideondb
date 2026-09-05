package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vectordb/vectordb/internal/api/rest"
	"github.com/vectordb/vectordb/internal/engine"
	"github.com/vectordb/vectordb/pkg/client"
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
	httpClient := inMemoryHTTPClient(rest.NewWithOptions(db, nil, rest.Options{APIKey: apiKey}).Handler())
	sdk, err := client.New("http://vectordb.test", client.Options{APIKey: apiKey, HTTPClient: httpClient})
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
	sdk, err := client.New("http://vectordb.test", client.Options{HTTPClient: httpClient})
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
