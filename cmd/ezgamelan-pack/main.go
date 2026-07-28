package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/r3t2/ezgamelan/internal/setup"
)

func main() {
	var executable string
	var payload string
	var output string
	flag.StringVar(&executable, "exe", "", "base Windows setup executable")
	flag.StringVar(&payload, "payload", "", "ZIP payload")
	flag.StringVar(&output, "output", "", "final setup executable")
	flag.Parse()
	if flag.NArg() != 0 || executable == "" || payload == "" || output == "" {
		fmt.Fprintln(
			os.Stderr,
			"usage: ezgamelan-pack -exe BASE.exe -payload PAYLOAD.zip -output SETUP.exe",
		)
		os.Exit(2)
	}
	if err := setup.Pack(executable, payload, output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
