package workflow

import (
	"context"
	"errors"
	"testing"
)

func TestCoordinatorRequiresStore(t *testing.T) {
	for _, c := range []*Coordinator{nil, {}, {Store: &Store{}}, {Store: &Store{Journal: &Journal{}}}} {
		if _, e := c.Tick(context.Background(), 1); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		if e := c.Run(context.Background(), nil); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
}
