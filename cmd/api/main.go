package main

import (
	"events-stocks/internal/app"
	"log/slog"
	"os"
)

func main() {
	if err := app.Run(); err != nil {
		slog.Error("api stopped with error", "error", err)
		os.Exit(1)
	}
}
