// Parity: framework/credentials/cli.py::main
package main

import (
	"os"

	"github.com/shark2202/go_hive/go-hive/internal/cli"
)

func main() {
	os.Exit(cli.GuardMain(func() int { return cli.NewCredRunner().Run(os.Args[1:]) }))
}
