package piagent

import (
	_ "embed"
	"os"
)

//go:embed rpc_launcher.mjs
var rpcLauncherSource []byte

func materializeRPCLauncher() (string, error) {
	file, err := os.CreateTemp("", "codingto-pi-rpc-*.mjs")
	if err != nil {
		return "", err
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.Write(rpcLauncherSource); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	keep = true
	return path, nil
}
