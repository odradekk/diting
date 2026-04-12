package bench

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads a bench query file and returns an unvalidated *QuerySet. Use
// LoadAndValidate when you need both parsing and schema checks.
//
// The YAML decoder is strict about unknown fields: a typo like
// `must_contain_domain` (missing `s`) will error out at parse time rather
// than silently decoding to zero values and confusing the downstream
// validator.
func Load(path string) (*QuerySet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &LoadError{Path: path, Err: fmt.Errorf("read: %w", err)}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var qs QuerySet
	if err := dec.Decode(&qs); err != nil {
		return nil, &LoadError{Path: path, Err: fmt.Errorf("parse: %w", err)}
	}
	// Reject multi-document YAML — bench files are a single document.
	var discard any
	if err := dec.Decode(&discard); err == nil {
		return nil, &LoadError{Path: path, Err: errors.New("parse: multi-document YAML not supported")}
	} else if !errors.Is(err, io.EOF) {
		return nil, &LoadError{Path: path, Err: fmt.Errorf("parse trailing: %w", err)}
	}
	return &qs, nil
}

// LoadAndValidate is Load + Validate.
func LoadAndValidate(path string) (*QuerySet, error) {
	qs, err := Load(path)
	if err != nil {
		return nil, err
	}
	if err := Validate(qs); err != nil {
		return nil, err
	}
	return qs, nil
}

// TotalQueries returns the total number of queries across all batches.
func (qs *QuerySet) TotalQueries() int {
	if qs == nil {
		return 0
	}
	n := 0
	for _, b := range qs.Batches {
		n += len(b.Queries)
	}
	return n
}

// FindByID returns the query with the given ID and its batch index, or
// (nil, -1) if not found.
func (qs *QuerySet) FindByID(id string) (*Query, int) {
	if qs == nil {
		return nil, -1
	}
	for bi := range qs.Batches {
		batch := &qs.Batches[bi]
		for qi := range batch.Queries {
			if batch.Queries[qi].ID == id {
				return &batch.Queries[qi], bi
			}
		}
	}
	return nil, -1
}
