// Command gideondb-mcp is a Model Context Protocol server for GideonDB. MCP
// clients (Claude Desktop, Claude Code, Cursor, agent SDKs) launch it as a
// subprocess and talk to it over stdio.
//
//	gideondb-mcp -url http://localhost:6333
//	gideondb-mcp -url http://localhost:6333 -allow-writes \
//	  -embed-url http://localhost:11434/v1 -embed-model nomic-embed-text
//
// Secrets come from the environment: GIDEONDB_API_KEY and EMBEDDING_API_KEY.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Bharanipbk/gideondb/internal/mcp"
	"github.com/Bharanipbk/gideondb/pkg/client"
)

var version = "dev"

func main() {
	url := flag.String("url", envOr("GIDEONDB_URL", "http://localhost:6333"), "GideonDB REST base URL")
	allowWrites := flag.Bool("allow-writes", false, "expose upsert, delete and memory write tools")
	allowAdmin := flag.Bool("allow-admin", false, "expose create_collection and delete_collection")
	embedURL := flag.String("embed-url", os.Getenv("EMBEDDING_URL"), "OpenAI-compatible embeddings base URL, e.g. https://api.openai.com/v1 or http://localhost:11434/v1")
	embedModel := flag.String("embed-model", os.Getenv("EMBEDDING_MODEL"), "embedding model name")
	memoryCollection := flag.String("memory-collection", "agent_memory", "collection that stores memories")
	flag.Parse()

	db, err := client.New(*url, client.Options{APIKey: os.Getenv("GIDEONDB_API_KEY")})
	if err != nil {
		fail(err)
	}
	cfg := mcp.Config{Client: db, AllowWrites: *allowWrites, AllowAdmin: *allowAdmin, MemoryCollection: *memoryCollection}
	if *embedURL != "" || *embedModel != "" {
		if *embedURL == "" || *embedModel == "" {
			fail(fmt.Errorf("-embed-url and -embed-model must be set together"))
		}
		cfg.Embedder = &mcp.OpenAIEmbedder{BaseURL: *embedURL, Model: *embedModel, APIKey: os.Getenv("EMBEDDING_API_KEY")}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// stdout carries protocol messages only; diagnostics go to stderr.
	if err := mcp.NewServer("gideondb", version, mcp.Tools(cfg)).Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		fail(err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gideondb-mcp:", err)
	os.Exit(1)
}
