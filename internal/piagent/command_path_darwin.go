//go:build darwin

package piagent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var prepareCommandPathOnce sync.Once

// prepareCommandPath restores the command locations commonly lost when an app
// is launched from Finder instead of an interactive shell. Version-manager
// directories are discovered once; command existence is still checked on each
// lookup, so a Node.pkg installation completed during this run is immediately
// visible through the already-added /usr/local/bin directory.
func prepareCommandPath() {
	prepareCommandPathOnce.Do(func() {
		preferred := make([]string, 0, 1)
		dirs := []string{
			"/usr/local/bin",
			"/opt/homebrew/bin",
			"/usr/local/sbin",
			"/opt/homebrew/sbin",
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			preferred = append(preferred, filepath.Join(managedNpmPrefix(), "bin"))
			dirs = append(dirs,
				filepath.Join(home, ".local", "bin"),
				filepath.Join(home, ".npm-global", "bin"),
				filepath.Join(home, ".volta", "bin"),
				filepath.Join(home, ".asdf", "shims"),
				filepath.Join(home, ".local", "share", "mise", "shims"),
				filepath.Join(home, "Library", "pnpm"),
			)
			for _, pattern := range []string{
				filepath.Join(home, ".nvm", "versions", "node", "*", "bin"),
				filepath.Join(home, ".fnm", "node-versions", "*", "installation", "bin"),
				filepath.Join(home, ".local", "share", "fnm", "node-versions", "*", "installation", "bin"),
				filepath.Join(home, "Library", "Application Support", "fnm", "node-versions", "*", "installation", "bin"),
			} {
				matches, _ := filepath.Glob(pattern)
				sortNodeVersionDirs(matches)
				dirs = append(dirs, matches...)
			}
		}

		seen := make(map[string]struct{})
		merged := make([]string, 0, len(dirs)+8)
		candidates := append(preferred, filepath.SplitList(os.Getenv("PATH"))...)
		candidates = append(candidates, dirs...)
		for _, dir := range candidates {
			dir = strings.TrimSpace(dir)
			if dir == "" {
				continue
			}
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			merged = append(merged, dir)
		}
		_ = os.Setenv("PATH", strings.Join(merged, string(os.PathListSeparator)))
	})
}
