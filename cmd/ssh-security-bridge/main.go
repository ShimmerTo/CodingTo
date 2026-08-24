package main

import (
	"os"

	"codingto/internal/sshsecuritybridge/bridge"
)

func main() { os.Exit(bridge.Run(os.Args[1:])) }
