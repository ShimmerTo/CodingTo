package sshsecurity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	knownHostsLockTimeout = 5 * time.Second
	knownHostsStaleLock   = 30 * time.Second
)

// KnownHosts persists verified host-key fingerprints by host:port so that a
// first successful handshake (TOFU) is remembered and enforced on later ones.
type KnownHosts struct {
	mu      sync.Mutex
	path    string
	entries map[string]string
	loadErr error
}

// LoadKnownHosts reads the persisted fingerprint map from path. An empty path
// returns nil so callers fall back to the strict default-reject callback.
// Malformed or unreadable files are retained as an error so verification fails
// closed instead of silently treating every server as a first contact.
func LoadKnownHosts(path string) *KnownHosts {
	if path == "" {
		return nil
	}
	entries, err := readKnownHostEntries(path)
	return &KnownHosts{path: path, entries: entries, loadErr: err}
}

func readKnownHostEntries(path string) (map[string]string, error) {
	entries := map[string]string{}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return entries, err
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return map[string]string{}, fmt.Errorf("known_hosts JSON 格式错误：%w", err)
	}
	return entries, nil
}

// Err reports whether the persisted fingerprint set could be loaded safely.
func (k *KnownHosts) Err() error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.loadErr
}

// Lookup returns the fingerprint previously recorded for hostPort.
func (k *KnownHosts) Lookup(hostPort string) (string, bool) {
	if k == nil {
		return "", false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	fingerprint, ok := k.entries[hostPort]
	return fingerprint, ok
}

// Record persists the observed fingerprint for hostPort. A cross-process lock
// protects the read-merge-write sequence shared by the app and bridge helpers;
// the destination is replaced atomically so readers never observe partial JSON.
func (k *KnownHosts) Record(hostPort, fingerprint string) error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.loadErr != nil {
		return k.loadErr
	}
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return err
	}
	release, err := acquireKnownHostsLock(k.path + ".lock")
	if err != nil {
		return err
	}
	defer release()

	entries, err := readKnownHostEntries(k.path)
	if err != nil {
		return err
	}
	for key, value := range k.entries {
		if current, exists := entries[key]; exists && current != value {
			return fmt.Errorf("SSH 主机 %s 的已记录指纹发生并发冲突", key)
		}
		entries[key] = value
	}
	if current, exists := entries[hostPort]; exists && current != fingerprint {
		return fmt.Errorf("SSH 主机密钥指纹已变化：已记录 %s，服务器提供 %s", current, fingerprint)
	}
	entries[hostPort] = fingerprint
	raw, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(k.path), ".ssh-known-hosts-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceKnownHostsFile(tempPath, k.path); err != nil {
		return err
	}
	k.entries = entries
	return nil
}

func acquireKnownHostsLock(path string) (func(), error) {
	deadline := time.Now().Add(knownHostsLockTimeout)
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > knownHostsStaleLock {
			if removeErr := os.Remove(path); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待 SSH known_hosts 文件锁超时")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
