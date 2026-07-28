package parser

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"codingto/internal/documentbridge/model"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

type ImageParser struct{}

func (ImageParser) Supports(kind model.FileKind) bool { return kind == model.KindImage }

func (ImageParser) Parse(ctx context.Context, source model.Source, sink Sink) (model.Summary, error) {
	file, err := os.Open(source.Path)
	if err != nil {
		return model.Summary{}, err
	}
	config, format, decodeErr := image.DecodeConfig(file)
	if _, err := file.Seek(0, 0); err != nil {
		file.Close()
		return model.Summary{}, err
	}
	extension := strings.ToLower(filepath.Ext(source.Path))
	mimeType := imageMIME(format, extension)
	if mimeType == "application/octet-stream" {
		mimeType = mime.TypeByExtension(extension)
	} else {
		extension = imageExtension(format, extension)
	}
	media := model.MediaMeta{
		ID: "img1", MIME: mimeType, Ext: extension, Size: source.Size,
		Page: 1, BlockID: "b1",
	}
	if decodeErr == nil {
		media.Width, media.Height = config.Width, config.Height
	}
	if err := sink.WriteMedia(ctx, media, file); err != nil {
		file.Close()
		return model.Summary{}, err
	}
	if err := file.Close(); err != nil {
		return model.Summary{}, err
	}
	description := fmt.Sprintf("图片 %s（%s，%dx%d）", filepath.Base(source.Path), mimeType, media.Width, media.Height)
	if err := sink.WriteBlock(ctx, model.Block{
		ID: "b1", Type: "image", Text: description, Page: 1, Ref: media.ID,
	}); err != nil {
		return model.Summary{}, err
	}
	return model.Summary{
		Pages: 1, PageKind: "physical", HasImage: true,
		Capabilities: model.Capabilities{Text: true, Tables: "none", Images: true, OCR: "local"},
	}, nil
}

func imageExtension(format, fallback string) string {
	switch strings.ToLower(format) {
	case "jpeg":
		return ".jpg"
	case "png", "gif", "webp", "bmp", "tiff":
		return "." + strings.ToLower(format)
	default:
		return fallback
	}
}

func imageMIME(format, extension string) string {
	switch strings.ToLower(format) {
	case "jpeg":
		return "image/jpeg"
	case "png", "gif", "webp", "bmp", "tiff":
		return "image/" + strings.ToLower(format)
	}
	switch extension {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".tif", ".tiff":
		return "image/tiff"
	default:
		return "application/octet-stream"
	}
}
