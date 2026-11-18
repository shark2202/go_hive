// Parity: framework/credentials/cli.py::main
package main

import (
	"os"

	"github.com/aden-hive/hive-go/internal/cli"
)

func main() {
	os.Exit(cli.NewCredRunner().Run(os.Args[1:]))
}
