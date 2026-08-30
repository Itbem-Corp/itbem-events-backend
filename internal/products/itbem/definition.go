package itbem

import "events-stocks/internal/products/core"

var Definition = core.Definition{
	Code:                    core.ITBEM,
	Name:                    "ITBEM",
	ProductLabel:            "Platform control plane",
	Modules:                 []string{"home", "users", "organizations", "metrics", "automation"},
	AllowsPlatformAuthority: true,
	SupportsEventOperations: false,
	SupportsAutomation:      true,
}
