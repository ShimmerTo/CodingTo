//go:build !windows && !darwin

package piagent

import "fmt"

// InstallNode is only implemented for Windows. On other platforms we ask the
// user to install Node.js manually because package managers differ per distro.
func InstallNode(onLog func(string)) error {
	return fmt.Errorf("当前平台不支持自动安装 Node.js，请手动安装 Node.js 后重试：https://nodejs.org")
}
