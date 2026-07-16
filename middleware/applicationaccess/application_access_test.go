package applicationaccess

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequiredSurfaceCapabilitySeparatesProducts(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   string
	}{
		{method: "GET", path: "/api/session", want: ""},
		{method: "GET", path: "/api/users", want: ""},
		{method: "POST", path: "/api/users/avatar", want: ""},
		{method: "GET", path: "/api/users/all", want: "platform:users:view"},
		{method: "PUT", path: "/api/users/abc", want: "platform:users:view"},
		{method: "GET", path: "/api/clients", want: "organizations:view|members:manage|events:manage"},
		{method: "POST", path: "/api/clients", want: "organizations:manage"},
		{method: "PUT", path: "/api/clients/abc", want: "organizations:manage"},
		{method: "GET", path: "/api/clients/members", want: "members:manage|applications:manage"},
		{method: "PUT", path: "/api/clients/abc/member-applications/user", want: "members:manage|applications:manage"},
		{method: "GET", path: "/api/events", want: "events:view"},
		{method: "GET", path: "/api/moments/activity", want: "events:view"},
		{method: "GET", path: "/api/catalogs/design-templates", want: "events:view"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			assert.Equal(t, test.want, requiredSurfaceCapability(test.method, test.path))
		})
	}
}
