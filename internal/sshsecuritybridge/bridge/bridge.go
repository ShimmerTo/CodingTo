package bridge

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"codingto/internal/sshsecuritybridge/protocol"
)

const bridgeVersion = "0.1.0"

// Run executes the ssh-security-bridge CLI.
func Run(args []string) int {
	if len(args) > 0 && args[0] == "ssh-security-bridge" {
		args = args[1:]
	}
	if len(args) < 1 {
		fail("用法：ssh-security-bridge serve --config <path>，或 version --json")
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
	configPath := flags.String("config", "", "0600 配置快照路径")
	if err := flags.Parse(arguments); err != nil {
		os.Exit(2)
	}
	if *configPath == "" {
		fail("serve 需要 --config")
	}
	service, err := New(*configPath, os.Getenv("CODINGTO_SSH_KNOWN_HOSTS"))
	if err != nil {
		fail("初始化服务失败：" + err.Error())
	}
	defer service.Close()
	if err := protocol.NewServer(service, os.Stdin, os.Stdout).Run(context.Background()); err != nil {
		fail(err.Error())
	}
}

func version(arguments []string) {
	value := map[string]any{"name": "ssh-security-bridge", "version": bridgeVersion, "protocolVersion": protocol.Version}
	if len(arguments) > 0 && arguments[0] == "--json" {
		raw, _ := json.Marshal(value)
		fmt.Println(string(raw))
		return
	}
	fmt.Printf("ssh-security-bridge %s (protocol %d)\n", bridgeVersion, protocol.Version)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
