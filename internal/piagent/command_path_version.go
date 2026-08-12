package piagent

import (
	"sort"
	"strconv"
	"strings"
)

// sortNodeVersionDirs orders version-manager command directories by semantic
// Node.js version, newest first. A lexical path sort would incorrectly place
// versions such as v20.9.0 ahead of v20.10.0.
func sortNodeVersionDirs(dirs []string) {
	sort.SliceStable(dirs, func(i, j int) bool {
		left, leftOK := nodeVersionFromPath(dirs[i])
		right, rightOK := nodeVersionFromPath(dirs[j])
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK {
			for index := 0; index < len(left) || index < len(right); index++ {
				leftPart := versionPart(left, index)
				rightPart := versionPart(right, index)
				if leftPart != rightPart {
					return leftPart > rightPart
				}
			}
		}
		return dirs[i] > dirs[j]
	})
}

func nodeVersionFromPath(path string) ([]int, bool) {
	parts := strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for index := len(parts) - 1; index >= 0; index-- {
		candidate := strings.TrimPrefix(parts[index], "v")
		versionParts := strings.Split(candidate, ".")
		if len(versionParts) < 2 {
			continue
		}

		version := make([]int, len(versionParts))
		valid := true
		for partIndex, part := range versionParts {
			if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				valid = false
				break
			}
			value, err := strconv.Atoi(part)
			if err != nil {
				valid = false
				break
			}
			version[partIndex] = value
		}
		if valid {
			return version, true
		}
	}
	return nil, false
}

func versionPart(version []int, index int) int {
	if index >= len(version) {
		return 0
	}
	return version[index]
}
