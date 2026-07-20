package cafettonhouse

import "events-stocks/internal/products/core"

var Definition = core.Definition{
	Code:                    core.CafettonHouse,
	Name:                    "Cafetton House",
	ProductLabel:            "Client operations",
	Modules:                 []string{"home", "users", "organizations", "metrics"},
	AllowsPlatformAuthority: false,
	SupportsEventOperations: false,
}
