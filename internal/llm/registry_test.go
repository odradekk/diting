package llm

import (
	"context"
	"testing"
)

type stubClient struct{}

func (s *stubClient) Complete(context.Context, Request) (*Response, error) {
	return &Response{Content: "ok"}, nil
}

func stubFactory(cfg ProviderConfig) (Client, error) {
	return &stubClient{}, nil
}

func TestRegister_And_Get(t *testing.T) {
	resetRegistry()
	Register("alpha", stubFactory)

	f, err := Get("alpha")
	if err != nil {
		t.Fatalf("Get(alpha): %v", err)
	}
	c, err := f(ProviderConfig{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	resp, err := c.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want ok", resp.Content)
	}
}

func TestGet_Unknown(t *testing.T) {
	resetRegistry()
	_, err := Get("nonexistent")
	if err == nil {
		t.Fatal("Get(nonexistent) should return error")
	}
}

func TestRegister_Duplicate_Panics(t *testing.T) {
	resetRegistry()
	Register("dup", stubFactory)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate register")
		}
	}()
	Register("dup", stubFactory)
}

func TestList(t *testing.T) {
	resetRegistry()
	Register("charlie", stubFactory)
	Register("alpha", stubFactory)
	Register("bravo", stubFactory)

	names := List()
	if len(names) != 3 {
		t.Fatalf("len(List) = %d, want 3", len(names))
	}
	if names[0] != "alpha" || names[1] != "bravo" || names[2] != "charlie" {
		t.Errorf("List = %v, want [alpha bravo charlie]", names)
	}
}
