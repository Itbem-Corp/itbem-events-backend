package itbem

import "events-stocks/internal/products/core"

var Definition = core.Definition{
	Code:                    core.ITBEM,
	Name:                    "ITBEM",
	ProductLabel:            "Business operations",
	Modules:                 []string{"home", "users", "organizations", "metrics", "automation"},
	AllowsPlatformAuthority: true,
	SupportsEventOperations: false,
	SupportsAutomation:      true,
}
