package nvr

import (
	"context"
	"errors"
	"testing"
)

func TestClassifyMotion(t *testing.T) {
	old := Classify
	t.Cleanup(func() { Classify = old })
	ctx := context.Background()

	Classify = func(ctx context.Context, jpeg []byte) ([]string, error) { return []string{"person", "car"}, nil }
	if l, fp := ClassifyMotion(ctx, []byte("jpg")); l != "person" || fp {
		t.Errorf("person: %q %v", l, fp)
	}
	Classify = func(ctx context.Context, jpeg []byte) ([]string, error) { return []string{"shadow"}, nil }
	if l, fp := ClassifyMotion(ctx, []byte("jpg")); l != "shadow" || !fp {
		t.Errorf("shadow should be a false positive: %q %v", l, fp)
	}
	Classify = func(ctx context.Context, jpeg []byte) ([]string, error) { return []string{"tree", "person"}, nil }
	if _, fp := ClassifyMotion(ctx, []byte("jpg")); fp {
		t.Errorf("person among labels: should not be filtered")
	}
	// Classifier failure: no label, no filtering (previous behavior).
	Classify = func(ctx context.Context, jpeg []byte) ([]string, error) { return nil, errors.New("boom") }
	if l, fp := ClassifyMotion(ctx, []byte("jpg")); l != "" || fp {
		t.Errorf("error: %q %v", l, fp)
	}
	Classify = func(ctx context.Context, jpeg []byte) ([]string, error) { return nil, nil }
	if l, fp := ClassifyMotion(ctx, []byte("jpg")); l != "" || fp {
		t.Errorf("empty: %q %v", l, fp)
	}
}
