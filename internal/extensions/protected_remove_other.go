//go:build !windows && !darwin

package extensions

func removeProtectedExecutable(_, _ string, removeErr error, _ func(string)) error {
	return removeErr
}
