package pagination

import (
	"reflect"
	"testing"
)

func TestPaginationContract(t *testing.T) {

	for _, tc := range []struct {
		name       string
		page, size int
		want       []string
	}{
		{"first", 1, 2, []string{"a", "b"}},
		{"second", 2, 2, []string{"c", "d"}},
		{"partial-tail", 3, 2, []string{"e"}},
		{"past-tail", 4, 2, []string{}},
		{"invalid-defaults", 0, 0, []string{"a", "b"}},
		{"oversized-limit", 1, 99, []string{"a", "b", "c", "d", "e"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []string{"a", "b", "c", "d", "e"}
			got := Page(items, tc.page, tc.size)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			if len(got) > 0 {
				got[0] = "changed"
				if !reflect.DeepEqual(items, []string{"a", "b", "c", "d", "e"}) {
					t.Fatal("page aliases source")
				}
			}
		})
	}
}
