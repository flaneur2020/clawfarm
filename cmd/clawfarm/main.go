package main

import (
	"fmt"
	"os"

	"github.com/yazhou/krunclaw/internal/app"
)

func main() {
	cli := app.New(os.Stdout, os.Stderr)
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "clawfarm: %v\n", err)
		os.Exit(1)
	}
}
