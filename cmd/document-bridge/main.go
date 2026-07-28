package main

import (
	"os"

	"codingto/internal/documentbridge/bridge"
)

func main() {
	os.Exit(bridge.Run(os.Args[1:]))
}
