package grpcapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
)

func TestAdminRPCsBridgeToRESTRoutes(t *testing.T) {
	var seen *http.Request
	var seenBody map[string]any
	routes := map[string]AdminRoute{
		"cluster_placement": {Method: http.MethodGet, Path: "/v1/cluster/placement", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r
			_, _ = w.Write([]byte(`{"epoch":7,"shards":[{"shard_id":0}]}`))
		})},
		"rebalance_prepare": {Method: http.MethodPost, Path: "/v1/cluster/rebalance/prepare", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&seenBody)
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"stale","message":"placement changed"}`))
		})},
		"logs": {Method: http.MethodGet, Path: "/v1/logs", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[{"msg":"a"},{"msg":"b"}]`))
		})},
	}
	server := &Server{options: Options{AdminRoutes: routes}}
	ctx := context.Background()

	response, err := server.GetPlacement(ctx, &v1.AdminRequest{Query: map[string]string{"collection": "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if seen.Method != http.MethodGet || seen.URL.Path != "/v1/cluster/placement" || seen.URL.Query().Get("collection") != "docs" {
		t.Fatalf("bridged request = %s %s", seen.Method, seen.URL)
	}
	if response.GetResult().AsMap()["epoch"] != float64(7) {
		t.Fatalf("result = %v", response.GetResult().AsMap())
	}

	body, _ := structpb.NewStruct(map[string]any{"plan_id": "p1"})
	_, err = server.PrepareRebalance(ctx, &v1.AdminRequest{Body: body})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "placement changed" {
		t.Fatalf("prepare error = %v", err)
	}
	if seenBody["plan_id"] != "p1" {
		t.Fatalf("body not forwarded: %v", seenBody)
	}

	logs, err := server.RecentLogs(ctx, &v1.AdminRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if items, _ := logs.GetResult().AsMap()["items"].([]any); len(items) != 2 {
		t.Fatalf("array not wrapped: %v", logs.GetResult().AsMap())
	}

	if _, err := server.ListRerankers(ctx, &v1.AdminRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("unconfigured route = %v", err)
	}
}

func TestAdminOnlyRPCsRejectNonAdmins(t *testing.T) {
	writer := &Principal{Name: "w", Role: "writer"}
	for method, rpc := range adminRPCs {
		err := authorize(writer, method, &v1.AdminRequest{})
		if rpc.adminOnly && status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s allowed a writer", method)
		}
		if !rpc.adminOnly && err != nil {
			t.Errorf("%s rejected a writer: %v", method, err)
		}
	}
	if err := authorize(&Principal{Role: "admin"}, v1.GideonDBService_ApplyRebalance_FullMethodName, &v1.AdminRequest{}); err != nil {
		t.Fatalf("admin rejected: %v", err)
	}
}
