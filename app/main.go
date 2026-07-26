package main

import (
	"fmt"
	"io"
	"os"
)

var revision = "unknown"

func main() {
	if err := printVersion(os.Stdout, revision); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
}

func printVersion(w io.Writer, rev string) error {
	if _, err := fmt.Fprintf(w, "revmux %s\n", rev); err != nil {
		return fmt.Errorf("write version: %w", err)
	}
	return nil
}
