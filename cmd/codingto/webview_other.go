//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

// webViewInstalled reports whether the platform webview backend is available.
// macOS ships WKWebView with the OS; the default Wails GTK4 build on Linux
// needs WebKitGTK 6.0.
func webViewInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		return true
	case "linux":
		if output, err := exec.Command("ldconfig", "-p").Output(); err == nil &&
			bytes.Contains(output, []byte("libwebkitgtk-6.0")) {
			return true
		}

		patterns := []string{
			"/usr/lib/*-linux-gnu/libwebkitgtk-6.0.so.*",
			"/usr/lib64/libwebkitgtk-6.0.so.*",
			"/usr/lib/libwebkitgtk-6.0.so.*",
			"/lib/*-linux-gnu/libwebkitgtk-6.0.so.*",
		}
		for _, pattern := range patterns {
			if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func showWebViewMissingDialog() {
	const url = "https://webkitgtk.org/"
	fmt.Println("CodingTo 需要 GTK4 和 WebKitGTK 6.0 才能在 Linux 上启动，但未检测到 WebKitGTK 6.0。")
	fmt.Println("Ubuntu 24.04 可运行：sudo apt install libgtk-4-1 libwebkitgtk-6.0-4")
	fmt.Printf("请安装对应依赖后重新启动程序。安装指引：%s\n", url)
	openInBrowser(url)
}

func openInBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	_ = cmd.Start()
}
