package main

import (
	"os"

	"sbx/internal/cli"
)

func main() {
	if code := cli.Execute(); code != 0 {
		os.Exit(code)
	}
}
