package subagentbridge

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

const bridgeVersion = "0.1.0"

// Run executes the subagent-bridge CLI. args may optionally start with the
// "subagent-bridge" kind token (used when the unified CodingTo executable
// self-spawns as the bridge); the token is stripped before dispatching.
func Run(args []string) int {
	if len(args) > 0 && args[0] == "subagent-bridge" {
		args = args[1:]
	}
	if len(args) < 1 {
		fail("usage: subagent-bridge serve --session-dir <dir> --work-dir <dir> --config <path>, or version --json")
	}
	switch args[0] {
	case "serve":
		serve(args[1:])
	case "version":
		version(args[1:])
	default:
		fail("unknown command: " + args[0])
	}
	return 0
}

func serve(arguments []string) {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	sessionDir := flags.String("session-dir", "", "CodingTo parent session directory")
	workDir := flags.String("work-dir", "", "trusted workspace directory")
	configPath := flags.String("config", "", "authorized subagent snapshot")
	if err := flags.Parse(arguments); err != nil {
		os.Exit(2)
	}
	if *sessionDir == "" || *workDir == "" || *configPath == "" {
		fail("serve requires --session-dir, --work-dir and --config")
	}
	snapshot, err := LoadSnapshot(*configPath)
	if err != nil {
		fail(err.Error())
	}
	if snapshot.SessionDir != *sessionDir || snapshot.WorkDir != *workDir {
		fail("subagent snapshot does not match serve directories")
	}
	server := NewServer(snapshot, os.Stdin, os.Stdout)
	if err := server.Run(context.Background()); err != nil {
		fail(err.Error())
	}
}

func version(arguments []string) {
	value := map[string]any{
		"name":            "subagent-bridge",
		"version":         bridgeVersion,
		"protocolVersion": ProtocolVersion,
	}
	if len(arguments) > 0 && arguments[0] == "--json" {
		raw, _ := json.Marshal(value)
		fmt.Println(string(raw))
		return
	}
	fmt.Printf("subagent-bridge %s (protocol %d)\n", bridgeVersion, ProtocolVersion)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
