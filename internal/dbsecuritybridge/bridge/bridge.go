package bridge

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"codingto/internal/dbsecuritybridge/config"
	"codingto/internal/dbsecuritybridge/connection"
	"codingto/internal/dbsecuritybridge/protocol"
	"codingto/internal/sshsecurity"
)

const bridgeVersion = "0.1.0"

// Run 执行 db-security-bridge CLI。args 可选以 "db-security-bridge"
// kind token 开头（统一可执行文件自举场景），先剥离再分发。
func Run(args []string) int {
	if len(args) > 0 && args[0] == "db-security-bridge" {
		args = args[1:]
	}
	if len(args) < 1 {
		fail("用法：db-security-bridge serve --config <path>，或 version --json / test-connection --config <path> --conn <id>")
	}
	switch args[0] {
	case "serve":
		serve(args[1:])
	case "version":
		version(args[1:])
	case "test-connection":
		testConnection(args[1:])
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
	svc, err := New(*configPath, os.Getenv("CODINGTO_SSH_KNOWN_HOSTS"))
	if err != nil {
		fail("初始化服务失败：" + err.Error())
	}
	defer svc.Close()
	server := protocol.NewServer(svc, os.Stdin, os.Stdout)
	// stdin EOF 即退出（Pi 结束会话）。
	if err := server.Run(context.Background()); err != nil {
		fail(err.Error())
	}
}

func version(arguments []string) {
	asJSON := len(arguments) > 0 && arguments[0] == "--json"
	value := map[string]any{
		"name":            "db-security-bridge",
		"version":         bridgeVersion,
		"protocolVersion": protocol.Version,
	}
	if asJSON {
		raw, _ := json.Marshal(value)
		fmt.Println(string(raw))
		return
	}
	fmt.Printf("db-security-bridge %s (protocol %d)\n", bridgeVersion, protocol.Version)
}

// testConnection 一次性连接测试：加载快照 → Ping → 输出 JSON 后退出。
// 供 App「测试连接」按钮调用，driver 依赖只在本二进制内。
func testConnection(arguments []string) {
	flags := flag.NewFlagSet("test-connection", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "0600 配置快照路径")
	connID := flags.String("conn", "", "连接 ID")
	if err := flags.Parse(arguments); err != nil {
		os.Exit(2)
	}
	if *configPath == "" || *connID == "" {
		fail("test-connection 需要 --config 与 --conn")
	}

	report := func(ok bool, message string) {
		raw, _ := json.Marshal(map[string]any{"ok": ok, "message": message})
		fmt.Println(string(raw))
		if !ok {
			os.Exit(1)
		}
	}

	snapshot := config.NewSnapshot(*configPath)
	cfg, err := snapshot.Config()
	if err != nil {
		report(false, err.Error())
	}
	conn, ok := cfg.ByID(*connID)
	if !ok {
		report(false, "连接不存在："+*connID)
	}
	manager := connection.NewManager(sshsecurity.LoadKnownHosts(os.Getenv("CODINGTO_SSH_KNOWN_HOSTS")))
	defer manager.Close()
	if _, err := manager.Get(context.Background(), conn); err != nil {
		report(false, err.Error())
	}
	report(true, "连接成功")
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
