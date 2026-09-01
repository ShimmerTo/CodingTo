//go:build darwin

package extensions

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func removeProtectedExecutable(path, displayName string, removeErr error, onLine func(string)) error {
	if !os.IsPermission(removeErr) {
		return removeErr
	}
	if onLine != nil {
		onLine("Requesting administrator permission to remove " + displayName + " at " + path)
	}
	const script = `on run argv
set targetPath to item 1 of argv
do shell script "/bin/rm -f -- " & quoted form of targetPath with administrator privileges
end run`
	cmd := exec.Command("/usr/bin/osascript", "-e", script, path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("administrator removal failed or was cancelled: %s", message)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("administrator removal completed but the file still exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("verify administrator removal: %w", err)
	}
	return nil
}
