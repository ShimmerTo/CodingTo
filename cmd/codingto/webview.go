package main

import "os"

// requireWebView ensures the platform webview runtime is available before the
// Wails window is created. When it is missing we show a native prompt and exit,
// because the application window cannot render without a webview.
func requireWebView() {
	if webViewInstalled() {
		return
	}
	showWebViewMissingDialog()
	os.Exit(0)
}
