package main

import (
	"context"
	"fmt"
	"log"

	"github.com/odradekk/diting/internal/fetch/utls"
)

func main() {
	f := utls.New(utls.Options{})
	result, err := f.Fetch(context.Background(), "https://en.wikipedia.org/wiki/Metasearch_engine")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Final URL: %s\n", result.FinalURL)
	fmt.Printf("ContentType: %s\n", result.ContentType)
	fmt.Printf("Lattency: %d\n", result.LatencyMs)
	fmt.Printf("Body: %s\n", result.Content)
}
