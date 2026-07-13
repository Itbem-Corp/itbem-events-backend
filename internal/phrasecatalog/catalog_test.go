package phrasecatalog

import (
	"strings"
	"testing"
)

func TestWeddingCatalogPreservesPublishedCorpusWithoutDuplicates(t *testing.T) {
	phrases := Wedding()
	if len(phrases) != 100 {
		t.Fatalf("Wedding() returned %d phrases, want 100", len(phrases))
	}
	seen := make(map[string]struct{}, len(phrases))
	for _, phrase := range phrases {
		phrase = strings.TrimSpace(phrase)
		if phrase == "" {
			t.Fatal("catalog contains an empty phrase")
		}
		if _, exists := seen[phrase]; exists {
			t.Fatalf("catalog contains duplicate phrase %q", phrase)
		}
		seen[phrase] = struct{}{}
	}
}

func TestForTypeReturnsDefensiveCopyAndUsefulFallbacks(t *testing.T) {
	wedding := ForType("BODA")
	wedding[0] = "mutated"
	if ForType("wedding")[0] == "mutated" {
		t.Fatal("ForType returned shared mutable storage")
	}
	if len(ForType("graduacion")) < 15 || len(ForType("unknown")) < 15 {
		t.Fatal("every public phrase category must satisfy the default response size")
	}
}
