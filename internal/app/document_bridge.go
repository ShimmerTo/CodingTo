package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func resolveDocumentBridgeBinary() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODINGTO_DOCUMENT_BRIDGE_BIN")); configured != "" {
		return validateDocumentBridgeBinary(configured)
	}
	executableName := "document-bridge"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), executableName))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "bin", executableName),
			filepath.Join(cwd, executableName),
		)
	}
	for _, candidate := range candidates {
		if path, err := validateDocumentBridgeBinary(candidate); err == nil {
			return path, nil
		}
	}
	// Fallback: the unified CodingTo executable can act as the bridge itself.
	if executable, err := os.Executable(); err == nil {
		if path, err := validateDocumentBridgeBinary(executable); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("Document Bridge helper 未安装；应与 CodingTo 一同构建并放在应用可执行文件旁")
}

func validateDocumentBridgeBinary(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Document Bridge helper 不是普通文件")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, absolute, "document-bridge", "version", "--json")
	configureDocumentBridgeProcess(command)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("Document Bridge helper 无法执行: %w", err)
	}
	var version struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(output, &version); err != nil || version.ProtocolVersion != 1 {
		return "", fmt.Errorf("Document Bridge helper 协议不兼容")
	}
	return absolute, nil
}

func documentPreviewRequest(event map[string]any) (documentID string, page int, ok bool) {
	if stringValue(event["type"]) != "tool_execution_start" || stringValue(event["toolName"]) != "codingto_document" {
		return "", 0, false
	}
	input := firstPresent(event["args"], event["input"], event["arguments"])
	params := mapValue(input)
	if len(params) == 0 {
		if raw, rawOK := input.(string); rawOK {
			_ = json.Unmarshal([]byte(raw), &params)
		}
	}
	if stringValue(params["action"]) != "preview" {
		return "", 0, false
	}
	documentID = stringValue(params["documentId"])
	if documentID == "" {
		return "", 0, false
	}
	page = int(intValue(params["page"]))
	if page < 1 {
		page = 1
	}
	return documentID, page, true
}
