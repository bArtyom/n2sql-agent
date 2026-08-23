package documenttag_test

import (
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documenttag"
)

func TestNormalizeIDsSortsAndDeduplicates(t *testing.T) {
	got, err := documenttag.NormalizeIDs([]int64{9, 2, 9, 4})
	if err != nil {
		t.Fatalf("NormalizeIDs() error = %v", err)
	}
	want := []int64{2, 4, 9}
	if len(got) != len(want) {
		t.Fatalf("NormalizeIDs() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("NormalizeIDs() = %v, want %v", got, want)
		}
	}
}

func TestNormalizeIDsRejectsInvalidInput(t *testing.T) {
	for _, input := range [][]int64{{0}, {-1}, make([]int64, documenttag.MaxTagIDs+1)} {
		if _, err := documenttag.NormalizeIDs(input); !errors.Is(err, documenttag.ErrInvalidTagIDs) {
			t.Fatalf("NormalizeIDs(%v) error = %v, want ErrInvalidTagIDs", input, err)
		}
	}
}

func TestValidateNameCanonicalizesWhitespace(t *testing.T) {
	got, err := documenttag.ValidateName("  Go Agent  ")
	if err != nil {
		t.Fatalf("ValidateName() error = %v", err)
	}
	if got != "Go Agent" {
		t.Fatalf("ValidateName() = %q, want %q", got, "Go Agent")
	}
}

func TestValidateNameRejectsNewlines(t *testing.T) {
	if _, err := documenttag.ValidateName("Go\nAgent"); !errors.Is(err, documenttag.ErrInvalidTagName) {
		t.Fatalf("ValidateName() error = %v, want ErrInvalidTagName", err)
	}
}
