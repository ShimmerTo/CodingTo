package sshsecurity

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"
)

// NormalizeHostKeyFingerprint canonicalizes the supported SHA256 fingerprint prefix.
func NormalizeHostKeyFingerprint(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= len("SHA256:") && strings.EqualFold(value[:len("SHA256:")], "SHA256:") {
		return "SHA256:" + value[len("SHA256:"):]
	}
	return value
}

// ValidateHostKeyFingerprint validates one OpenSSH SHA256 host-key fingerprint.
// Empty values are accepted for persistence but rejected by HostKeyCallback before authentication.
func ValidateHostKeyFingerprint(value string) error {
	value = NormalizeHostKeyFingerprint(value)
	if value == "" {
		return nil
	}
	if !strings.HasPrefix(value, "SHA256:") {
		return fmt.Errorf("SSH 主机密钥指纹必须使用 SHA256: 格式")
	}
	digest, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "SHA256:"))
	if err != nil || len(digest) != 32 {
		return fmt.Errorf("SSH 主机密钥指纹格式不合法")
	}
	return nil
}

// HostKeyCallback returns a callback that enforces one OpenSSH SHA256 host-key fingerprint.
// When expected is set it rejects any mismatch before authentication. When expected is empty
// and known is non-nil it applies TOFU: a host:port seen before must present the recorded
// fingerprint, while a first contact is accepted and recorded. With neither set it rejects the
// handshake and reports the observed fingerprint so the user can verify it through a trusted
// channel before persisting it.
func HostKeyCallback(expected string, known *KnownHosts) (ssh.HostKeyCallback, error) {
	expected = NormalizeHostKeyFingerprint(expected)
	if err := ValidateHostKeyFingerprint(expected); err != nil {
		return nil, err
	}
	return func(hostPort string, _ net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if expected != "" {
			if len(actual) != len(expected) || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
				return fmt.Errorf("SSH 主机密钥指纹不匹配：期望 %s，实际 %s", expected, actual)
			}
			return nil
		}
		if known != nil {
			if err := known.Err(); err != nil {
				return fmt.Errorf("读取 SSH 主机密钥记录失败：%w", err)
			}
			if stored, ok := known.Lookup(hostPort); ok {
				if len(actual) != len(stored) || subtle.ConstantTimeCompare([]byte(actual), []byte(stored)) != 1 {
					return fmt.Errorf("SSH 主机密钥指纹已变化：已记录 %s，服务器提供 %s", stored, actual)
				}
				return nil
			}
			if err := known.Record(hostPort, actual); err != nil {
				return fmt.Errorf("记录 SSH 主机密钥指纹失败：%w", err)
			}
			return nil
		}
		return fmt.Errorf("未配置 SSH 主机密钥指纹；服务器提供 %s，请核对后保存", actual)
	}, nil
}
