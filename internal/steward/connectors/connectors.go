// Package connectors provides the IM channel connectors for the steward:
// DingTalk Stream (WebSocket), Feishu long connection (WebSocket), WeCom
// callback (HTTP server), and a local loopback channel for testing.
//
// Connectors implement steward.Connector and are wired into the steward
// service by the Wails app through Factories().
package connectors

import (
	"fmt"
	"strconv"

	"codingto/internal/steward"
)

// Config key names used by the frontend channel forms and connectors.
const (
	KeyClientID       = "clientId"
	KeyClientSecret   = "clientSecret" // secret
	KeyAppID          = "appId"
	KeyAppSecret      = "appSecret" // secret
	KeyCorpID         = "corpId"
	KeyAgentID        = "agentId"
	KeySecret         = "secret" // secret
	KeyToken          = "token"
	KeyEncodingAESKey = "encodingAESKey"
	KeyCallbackURL    = "callbackUrl"
)

// Factories returns the connector factory for every supported platform.
func Factories() map[steward.Platform]steward.ConnectorFactory {
	return map[steward.Platform]steward.ConnectorFactory{
		steward.PlatformLoopback: loopbackFactory,
		steward.PlatformDingTalk: dingtalkFactory,
		steward.PlatformFeishu:   feishuFactory,
		steward.PlatformWeCom:    wecomFactory,
	}
}

// PlatformName maps a platform to its display name.
func PlatformName(platform string) string {
	switch steward.Platform(platform) {
	case steward.PlatformDingTalk:
		return "钉钉"
	case steward.PlatformFeishu:
		return "飞书"
	case steward.PlatformWeCom:
		return "企业微信"
	case steward.PlatformLoopback:
		return "本地测试"
	default:
		return platform
	}
}

// Supported reports whether the platform has a registered connector.
func Supported(platform string) bool {
	switch steward.Platform(platform) {
	case steward.PlatformDingTalk, steward.PlatformFeishu, steward.PlatformWeCom, steward.PlatformLoopback:
		return true
	default:
		return false
	}
}

func required(config map[string]string, key string) error {
	if config[key] == "" {
		return fmt.Errorf("缺少配置项 %s", key)
	}
	return nil
}

func channelIDFromConfig(config map[string]string) int64 {
	id, err := strconv.ParseInt(config["channelId"], 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}
