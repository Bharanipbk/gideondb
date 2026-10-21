// Command lite-go embeds GideonDB in a Go program: no server, no network.
//
//	go run ./examples/lite-go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/Bharanipbk/gideondb/pkg/lite"
)

func main() {
	dir, err := os.MkdirTemp("", "gideondb-lite-example-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db, err := lite.Open(dir, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := db.CreateCollection(lite.Collection{Name: "notes", Dimension: 3, Metric: lite.Cosine}); err != nil {
		log.Fatal(err)
	}
	err = db.Upsert("notes",
		lite.Record{ID: "1", Vector: []float32{1, 0, 0}, Text: "Go makes concurrency simple", Metadata: map[string]any{"topic": "go"}},
		lite.Record{ID: "2", Vector: []float32{0.9, 0.1, 0}, Text: "Python is great for notebooks", Metadata: map[string]any{"topic": "python"}},
		lite.Record{ID: "3", Vector: []float32{0, 0, 1}, Text: "Vector databases store embeddings", Metadata: map[string]any{"topic": "db"}},
	)
	if err != nil {
		log.Fatal(err)
	}

	show := func(label string, q lite.Query) {
		hits, err := db.Search("notes", q)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(label)
		for _, h := range hits {
			fmt.Printf("  %s  %.3f\n", h.ID, h.Score)
		}
	}
	show("dense:", lite.Query{Vector: []float32{1, 0.05, 0}, TopK: 2})
	show("filtered:", lite.Query{Vector: []float32{1, 0.05, 0}, TopK: 2, Filter: map[string]any{"topic": "python"}})
	show("keyword (BM25):", lite.Query{Text: "embeddings", TopK: 1})
	show("hybrid (RRF):", lite.Query{Vector: []float32{0, 0, 1}, Text: "concurrency", TopK: 2, Fusion: lite.FusionRRF})
}
