// Command keypoint is the Keypoint Notify hub and its command-line client in
// one binary. `kp serve` runs the server; every other subcommand talks to one.
package main

import (
	"os"

	"github.com/light/keypoint-notify/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
