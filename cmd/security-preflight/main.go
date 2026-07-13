package main

import (
	"fmt"
	"os"

	"events-stocks/configuration"
)

func main() {
	if err := configuration.ValidateSecurityConfiguration(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("security configuration valid")
}
