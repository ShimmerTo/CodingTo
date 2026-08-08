//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// webview2ClientID is the EdgeUpdate client id for the WebView2 runtime.
const webview2ClientID = "{F3017226-FE2A-4295-8BDF-91569798C18B}"

// webViewInstalled reports whether the WebView2 runtime is present on Windows.
//
// Detection is intentionally defensive: depending on how the runtime was
// installed (machine-wide vs per-user, Evergreen vs standalone, and which
// registry view it landed in) the registration can appear under several
// different keys. We probe all of them and also fall back to the on-disk
// runtime directory. When everything fails we write a small diagnostic file
// so the exact probe results can be inspected.
func webViewInstalled() bool {
	// Always inspect the 64-bit registry view so detection behaves the same
	// for 32-bit and 64-bit builds, and regardless of where EdgeUpdate wrote
	// the registration.
	access := uint32(registry.READ | registry.WOW64_64KEY)
	roots := []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER}
	rootNames := map[registry.Key]string{
		registry.LOCAL_MACHINE: "HKLM",
		registry.CURRENT_USER:  "HKCU",
	}

	checked := make([]string, 0)

	// Strategy 1 & 2: EdgeUpdate client registration (standalone + evergreen).
	edgeKeys := []string{
		`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webview2ClientID,
		`SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webview2ClientID,
	}
	for _, root := range roots {
		for _, path := range edgeKeys {
			checked = append(checked, rootNames[root]+`\`+path)
			k, err := registry.OpenKey(root, path, access)
			if err != nil {
				continue
			}
			if v, _, err := k.GetStringValue("pv"); err == nil {
				k.Close()
				writeWebViewDiag(checked, "found EdgeUpdate client, pv="+v)
				return true
			}
			k.Close()
		}
	}

	// Strategy 3: EdgeWebView Applications registry (runtime install location).
	appKeys := []string{
		`SOFTWARE\WOW6432Node\Microsoft\EdgeWebView\Applications`,
		`SOFTWARE\Microsoft\EdgeWebView\Applications`,
	}
	for _, root := range roots {
		for _, path := range appKeys {
			checked = append(checked, rootNames[root]+`\`+path)
			k, err := registry.OpenKey(root, path, access)
			if err != nil {
				continue
			}
			names, err := k.ReadSubKeyNames(-1)
			k.Close()
			if err == nil && len(names) > 0 {
				writeWebViewDiag(checked, "found EdgeWebView Applications: "+strings.Join(names, ","))
				return true
			}
		}
	}

	// Strategy 4: on-disk runtime directory.
	if dir := webViewRuntimeDir(); dir != "" {
		writeWebViewDiag(checked, "found runtime directory: "+dir)
		return true
	}

	writeWebViewDiag(checked, "NOT installed")
	return false
}

// webViewRuntimeDir returns the path of an installed WebView2 runtime
// directory if one exists, otherwise an empty string. It covers both the
// machine-wide location (under ProgramFiles) and the per-user location
// (under LocalAppData).
func webViewRuntimeDir() string {
	bases := []string{
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("ProgramFiles"),
		os.Getenv("LOCALAPPDATA"),
	}
	for _, base := range bases {
		if base == "" {
			continue
		}
		dir := filepath.Join(base, "Microsoft", "EdgeWebView", "Application")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// writeWebViewDiag appends a diagnostic record so a failed detection can be
// inspected. It never affects program flow.
func writeWebViewDiag(checked []string, result string) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "codingto_webview_diag.txt"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(f, "[%s] result=%s\n", ts, result)
	for _, c := range checked {
		fmt.Fprintf(f, "  checked: %s\n", c)
	}
}

func showWebViewMissingDialog() {
	user32 := syscall.NewLazyDLL("user32.dll")
	msgBox := user32.NewProc("MessageBoxW")
	title, _ := syscall.UTF16PtrFromString("CodingTo")
	text, _ := syscall.UTF16PtrFromString("当前未安装 WebView2 运行时，CodingTo 无法启动。\n是否前往微软官网下载安装？")
	// MB_YESNO = 4, MB_ICONQUESTION = 0x20
	ret, _, _ := msgBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 4|0x20)
	if ret == 6 { // IDYES
		openInBrowser("https://developer.microsoft.com/zh-cn/microsoft-edge/webview2/")
	}
}

func openInBrowser(url string) {
	cmd := exec.Command("cmd", "/c", "start", "", url)
	// 隐藏 cmd 控制台窗口：直接执行 cmd /c start 会闪一个黑框。
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	_ = cmd.Start()
}
