package main

import (
	"os"

	"codingto/internal/subagentbridge"
)

func main() {
	os.Exit(subagentbridge.Run(os.Args[1:]))
}
