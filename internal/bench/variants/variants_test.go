package variants

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/odradekk/diting/internal/bench"
)

// stubVariant is a trivial bench.Variant used to populate the registry
// in tests. It returns a canned Result for any RunInput.
type stubVariant struct{ name string }

func (s *stubVariant) Name() string { return s.name }
func (s *stubVariant) Run(_ context.Context, in bench.RunInput) (bench.Result, error) {
	return bench.Result{QueryID: in.ID, Answer: "stub for " + in.ID}, nil
}

func stubFactory(name string) Factory {
	return func() (bench.Variant, error) { return &stubVariant{name: name}, nil }
}

func TestRegister_Get(t *testing.T) {
	resetRegistry()
	Register("alpha", stubFactory("alpha"))

	f, err := Get("alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	v, err := f()
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if v.Name() != "alpha" {
		t.Errorf("Name() = %q, want alpha", v.Name())
	}
}

func TestRegister_DuplicatePanics(t *testing.T) {
	resetRegistry()
	Register("dup", stubFactory("dup"))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate Register")
		}
		if !strings.Contains(r.(string), "duplicate") {
			t.Errorf("panic message = %v", r)
		}
	}()
	Register("dup", stubFactory("dup-second"))
}

func TestGet_Unknown(t *testing.T) {
	resetRegistry()
	_, err := Get("nothing-here")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown variant") {
		t.Errorf("error should say 'unknown variant': %v", err)
	}
	if !strings.Contains(err.Error(), "nothing-here") {
		t.Errorf("error should include the name: %v", err)
	}
}

func TestList_Sorted(t *testing.T) {
	resetRegistry()
	Register("charlie", stubFactory("charlie"))
	Register("alpha", stubFactory("alpha"))
	Register("bravo", stubFactory("bravo"))

	got := List()
	want := []string{"alpha", "bravo", "charlie"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %v, want %v", got, want)
	}
	// Verify it's actually sorted (not just coincidentally).
	if !sort.StringsAreSorted(got) {
		t.Error("List() result is not sorted")
	}
}

func TestList_Empty(t *testing.T) {
	resetRegistry()
	got := List()
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestFactory_CanFail(t *testing.T) {
	resetRegistry()
	sentinel := errors.New("init failed")
	Register("bad", func() (bench.Variant, error) { return nil, sentinel })

	f, err := Get("bad")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_, err = f()
	if !errors.Is(err, sentinel) {
		t.Errorf("factory err = %v, want %v", err, sentinel)
	}
}
