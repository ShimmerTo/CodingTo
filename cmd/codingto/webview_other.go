//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// webViewInstalled reports whether the platform webview backend is available.
// macOS ships WKWebView with the OS; Linux needs WebKitGTK 4.1.
func webViewInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		return true
	case "linux":
		candidates := []string{
			"/usr/lib/x86_64-linux-gnu/libwebkit2gtk-4.1.so.0",
			"/usr/lib64/libwebkit2gtk-4.1.so.0",
			"/usr/lib/libwebkit2gtk-4.1.so.0",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return true
			}
		}
		if err := exec.Command("sh", "-c", "ldconfig -p | grep -q webkit2gtk-4.1").Run(); err == nil {
			return true
		}
		return false
	default:
		return true
	}
}

func showWebViewMissingDialog() {
	const url = "https://webkitgtk.org/"
	fmt.Println("CodingTo 需要系统 WebView 运行时（Linux 需安装 webkit2gtk-4.1）才能启动，但未检测到。")
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
