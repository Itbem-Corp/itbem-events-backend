package core

import "strings"

type Code string

const (
	EventiApp     Code = "eventiapp"
	ITBEM         Code = "itbem"
	CafettonHouse Code = "cafettonhouse"
)

type Definition struct {
	Code                    Code
	Name                    string
	ProductLabel            string
	Modules                 []string
	AllowsPlatformAuthority bool
	SupportsEventOperations bool
	SupportsAutomation      bool
}

func (c Code) String() string { return string(c) }

func Normalize(value string) Code {
	return Code(strings.ToLower(strings.TrimSpace(value)))
}
