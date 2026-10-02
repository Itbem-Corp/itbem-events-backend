package app

import (
	"net"
	"testing"
)

func TestSyntheticIdentityAPIListensOnlyOnLoopback(t *testing.T) {
	listener, err := net.Listen("tcp", httpListenAddress(" LOCAL ", "http://127.0.0.1:19090", "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if address, ok := listener.Addr().(*net.TCPAddr); !ok || !address.IP.IsLoopback() {
		t.Fatalf("synthetic identity API exposed a non-loopback listener: %v", listener.Addr())
	}
}

func TestProductionAndOrdinaryDevelopmentRetainTheirListener(t *testing.T) {
	for _, test := range []struct{ environment, issuer string }{
		{"production", ""}, {"staging", ""}, {"local", ""},
		{"production", "http://127.0.0.1:19090"},
	} {
		if got := httpListenAddress(test.environment, test.issuer, "8080"); got != ":8080" {
			t.Fatalf("ordinary listener changed for %s: %s", test.environment, got)
		}
	}
}
