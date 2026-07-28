package bridge

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"codingto/internal/documentbridge/protocol"
	"codingto/internal/documentbridge/service"
)

const bridgeVersion = "0.1.0"

// Run executes the document-bridge CLI. args may optionally start with the
// "document-bridge" kind token (used when the unified CodingTo executable
// self-spawns as the bridge); the token is stripped before dispatching.
func Run(args []string) int {
	if len(args) > 0 && args[0] == "document-bridge" {
		args = args[1:]
	}
	if len(args) < 1 {
		fail("用法：document-bridge serve --session-dir <dir> --work-dir <dir>，或 version --json")
	}
	switch args[0] {
	case "serve":
		serve(args[1:])
	case "version":
		version(args[1:])
	default:
		fail("未知命令：" + args[0])
	}
	return 0
}

func serve(arguments []string) {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	sessionDir := flags.String("session-dir", "", "CodingTo session directory")
	workDir := flags.String("work-dir", "", "trusted workspace directory")
	if err := flags.Parse(arguments); err != nil {
		os.Exit(2)
	}
	if *sessionDir == "" || *workDir == "" {
		fail("serve 需要 --session-dir 和 --work-dir")
	}
	svc, err := service.New(*sessionDir, *workDir)
	if err != nil {
		fail("初始化服务失败：" + err.Error())
	}
	server := protocol.NewServer(svc, os.Stdin, os.Stdout)
	if err := server.Run(context.Background()); err != nil {
		fail(err.Error())
	}
}

func version(arguments []string) {
	asJSON := len(arguments) > 0 && arguments[0] == "--json"
	value := map[string]any{
		"name":            "document-bridge",
		"version":         bridgeVersion,
		"protocolVersion": protocol.Version,
	}
	if asJSON {
		raw, _ := json.Marshal(value)
		fmt.Println(string(raw))
		return
	}
	fmt.Printf("document-bridge %s (protocol %d)\n", bridgeVersion, protocol.Version)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
