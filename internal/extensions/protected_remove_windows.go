//go:build windows

package extensions

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode/utf16"
)

func removeProtectedExecutable(path, displayName string, removeErr error, onLine func(string)) error {
	if !os.IsPermission(removeErr) {
		return removeErr
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		powershell, err = exec.LookPath("pwsh.exe")
	}
	if err != nil {
		return fmt.Errorf("administrator removal requires PowerShell: %w", removeErr)
	}
	if onLine != nil {
		onLine("Requesting administrator permission to remove " + displayName + " at " + path)
	}
	escapedPath := strings.ReplaceAll(path, "'", "''")
	innerScript := "$target='" + escapedPath + "'; if (Test-Path -LiteralPath $target -PathType Leaf) { Remove-Item -LiteralPath $target -Force -ErrorAction Stop }"
	encoded := encodePowerShellCommand(innerScript)
	escapedPowerShell := strings.ReplaceAll(powershell, "'", "''")
	outerScript := "$process=Start-Process -FilePath '" + escapedPowerShell + "' -ArgumentList @('-NoProfile','-NonInteractive','-EncodedCommand','" + encoded + "') -Verb RunAs -Wait -PassThru; exit $process.ExitCode"
	if _, err := runUnboundedWithProgress(onLine, powershell, "-NoProfile", "-NonInteractive", "-Command", outerScript); err != nil {
		return fmt.Errorf("administrator removal failed or was cancelled: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("administrator removal completed but the file still exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("verify administrator removal: %w", err)
	}
	return nil
}

func encodePowerShellCommand(command string) string {
	units := utf16.Encode([]rune(command))
	raw := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(raw[index*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
