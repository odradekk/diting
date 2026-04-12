package search

import (
	"context"
	"testing"
)

// stubModule implements Module for registry tests.
type stubModule struct {
	manifest Manifest
}

func (s *stubModule) Manifest() Manifest                                        { return s.manifest }
func (s *stubModule) Search(context.Context, string) ([]SearchResult, error) { return nil, nil }
func (s *stubModule) Close() error                                              { return nil }

func stubFactory(name string, st SourceType) Factory {
	return func(cfg ModuleConfig) (Module, error) {
		return &stubModule{manifest: Manifest{
			Name:       name,
			SourceType: st,
			CostTier:   CostTierFree,
		}}, nil
	}
}

func TestRegister_And_Get(t *testing.T) {
	resetRegistry()
	Register("alpha", stubFactory("alpha", SourceTypeGeneralWeb))

	f, err := Get("alpha")
	if err != nil {
		t.Fatalf("Get(alpha): %v", err)
	}
	m, err := f(ModuleConfig{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if m.Manifest().Name != "alpha" {
		t.Errorf("Name = %q, want alpha", m.Manifest().Name)
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
	Register("dup", stubFactory("dup", SourceTypeCode))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate register")
		}
	}()
	Register("dup", stubFactory("dup", SourceTypeCode))
}

func TestList(t *testing.T) {
	resetRegistry()
	Register("charlie", stubFactory("charlie", SourceTypeCommunity))
	Register("alpha", stubFactory("alpha", SourceTypeGeneralWeb))
	Register("bravo", stubFactory("bravo", SourceTypeAcademic))

	names := List()
	if len(names) != 3 {
		t.Fatalf("len(List) = %d, want 3", len(names))
	}
	// Should be sorted.
	if names[0] != "alpha" || names[1] != "bravo" || names[2] != "charlie" {
		t.Errorf("List = %v, want [alpha bravo charlie]", names)
	}
}

func TestList_Empty(t *testing.T) {
	resetRegistry()
	names := List()
	if len(names) != 0 {
		t.Errorf("List = %v, want empty", names)
	}
}
