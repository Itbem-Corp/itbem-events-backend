package cacheutil

import "testing"

func TestTenantKeySeparatesProductAndUserScopes(t *testing.T) {
	itbem := TenantKey("itbem", "user:42", "myclients")
	cafetton := TenantKey("cafettonhouse", "user:42", "myclients")
	if itbem == cafetton {
		t.Fatal("different products must never share a personalized cache key")
	}
	if want := "v1:tenant:itbem:user:42:myclients"; itbem != want {
		t.Fatalf("key = %q, want %q", itbem, want)
	}
}
