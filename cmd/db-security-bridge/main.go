package main

import (
	"os"

	"codingto/internal/dbsecuritybridge/bridge"
)

func main() {
	os.Exit(bridge.Run(os.Args[1:]))
}
