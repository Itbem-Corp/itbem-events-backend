package app

import (
	"net"
	"strings"
)

func httpListenAddress(environment, fixtureIssuer, port string) string {
	if strings.EqualFold(strings.TrimSpace(environment), "local") && strings.TrimSpace(fixtureIssuer) != "" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return ":" + port
}
