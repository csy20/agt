package main

import (
	"os"

	"github.com/csy20/agt/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args))
}
