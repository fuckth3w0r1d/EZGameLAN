package main

import (
	"fmt"
	"os"

	"github.com/r3t2/ezgamelan/internal/portable"
)

var payloadManifest string

func main() {
	if err := portable.RunBackend(os.Args[1:], payloadManifest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
