package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"codingto/internal/piagent"
)

func validatePromptContent(req PromptRequest) error {
	if strings.TrimSpace(req.Message) == "" && len(req.Images) == 0 && len(req.Attachments) == 0 {
		return errors.New("message, images, and attachments are empty")
	}
	return nil
}

func validateImages(images []ImageInput) error {
	// Limits match the attachment guard rails
	// (docs/design/附件上传、输入产物与多模态传递设计.md §6): 50 MB each, 100 MB total.
	const maxBytes = 50 * 1024 * 1024
	const maxTotal = 100 * 1024 * 1024
	if len(images) > 10 {
		return errors.New("send at most 10 images")
	}
	var total int64
	for _, image := range images {
		if image.Type != "image" || !strings.HasPrefix(image.MimeType, "image/") {
			return fmt.Errorf("invalid image attachment: %s", image.Name)
		}
		decoded, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			return fmt.Errorf("invalid image data: %s", image.Name)
		}
		if len(decoded) > maxBytes {
			return fmt.Errorf("image is larger than 50 MB: %s", image.Name)
		}
		total += int64(len(decoded))
	}
	if total > maxTotal {
		return errors.New("total image size exceeds 100 MB")
	}
	return nil
}

func validateProviderCredentials(providers []piagent.Provider, providerName string) error {
	for _, provider := range providers {
		if provider.Name != providerName {
			continue
		}
		key := strings.TrimSpace(provider.APIKey)
		if !strings.HasPrefix(key, "$") {
			return nil
		}
		if resolveAPIKey(key) == "" {
			return fmt.Errorf("API key environment variable %s is not set for provider %s", strings.Trim(key, "${} "), provider.Name)
		}
		return nil
	}
	return nil
}
