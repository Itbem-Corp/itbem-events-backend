package eventiapp

import "events-stocks/internal/products/core"

var Definition = core.Definition{
	Code:                    core.EventiApp,
	Name:                    "EventiApp",
	ProductLabel:            "Event operations",
	Modules:                 []string{"home", "events", "metrics"},
	AllowsPlatformAuthority: true,
	SupportsEventOperations: true,
	SupportsAutomation:      false,
}
