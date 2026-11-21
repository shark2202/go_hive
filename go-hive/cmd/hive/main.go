// Parity: framework/cli.py::main
package main

import (
	"os"

	"github.com/aden-hive/hive-go/internal/cli"
)

func main() {
	os.Exit(cli.GuardMain(func() int { return cli.NewRunner().Run(os.Args[1:]) }))
}
