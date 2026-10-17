package main

import (
	"os"

	"github.com/mewebstudio/conductor-cli/internal/cli"
)

// Set by the release build: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
