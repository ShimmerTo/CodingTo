package ocr

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

type Engine struct {
	backend string
	bin     string
	langs   string
	custom  func(context.Context, string) (string, error)
}

// New creates an in-process OCR backend. It is useful for embedding a
// platform-specific engine without changing parser or artifact-store code.
func New(name string, recognize func(context.Context, string) (string, error)) Engine {
	return Engine{backend: strings.TrimSpace(name), custom: recognize}
}

func Discover() Engine {
	if configured := strings.TrimSpace(os.Getenv("CODINGTO_OCR_BIN")); configured != "" {
		return Engine{backend: "tesseract", bin: configured, langs: configuredLanguages(configured)}
	}
	for _, name := range []string{"tesseract", "tesseract.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return Engine{backend: "tesseract", bin: path, langs: configuredLanguages(path)}
		}
	}
	if runtime.GOOS == "windows" {
		if path, err := exec.LookPath("powershell.exe"); err == nil {
			return Engine{backend: "windows-ocr", bin: path}
		}
	}
	return Engine{}
}

func (e Engine) Available() bool { return e.backend != "" }

func (e Engine) Name() string {
	if e.backend == "" {
		return "unavailable"
	}
	if e.langs != "" {
		return e.backend + ":" + e.langs
	}
	return e.backend
}

func (e Engine) Recognize(ctx context.Context, imagePath string) (string, error) {
	if e.custom != nil {
		text, err := e.custom(ctx, imagePath)
		return normalize(text), err
	}
	switch e.backend {
	case "tesseract":
		args := []string{imagePath, "stdout"}
		if e.langs != "" {
			args = append(args, "-l", e.langs)
		}
		args = append(args, "--psm", "6")
		cmd := exec.CommandContext(ctx, e.bin, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			diagnostic := strings.ReplaceAll(stderr.String(), imagePath, "<image>")
			return "", fmt.Errorf("tesseract failed: %s", bounded(diagnostic, 500))
		}
		return normalize(string(output)), nil
	case "windows-ocr":
		cmd := exec.CommandContext(ctx, e.bin, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", windowsOCRScript)
		cmd.Env = append(os.Environ(), "CODINGTO_OCR_IMAGE="+imagePath)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			diagnostic := strings.ReplaceAll(stderr.String(), imagePath, "<image>")
			return "", fmt.Errorf("Windows OCR failed: %s", bounded(diagnostic, 500))
		}
		return normalize(string(output)), nil
	default:
		return "", fmt.Errorf("no local OCR engine is available")
	}
}

func configuredLanguages(bin string) string {
	if configured := strings.TrimSpace(os.Getenv("CODINGTO_OCR_LANGUAGES")); configured != "" {
		return configured
	}
	cmd := exec.Command(bin, "--list-langs")
	output, err := cmd.Output()
	if err != nil {
		return "eng"
	}
	available := map[string]bool{}
	for _, line := range strings.Fields(string(output)) {
		available[strings.TrimSpace(line)] = true
	}
	preferred := []string{}
	for _, language := range []string{"eng", "chi_sim", "chi_tra"} {
		if available[language] {
			preferred = append(preferred, language)
		}
	}
	if len(preferred) == 0 {
		keys := make([]string, 0, len(available))
		for language := range available {
			if language != "List" && language != "of" && language != "available" && language != "languages" {
				keys = append(keys, language)
			}
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			return keys[0]
		}
		return ""
	}
	return strings.Join(preferred, "+")
}

func normalize(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func bounded(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}

const windowsOCRScript = `
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new()
Add-Type -AssemblyName System.Runtime.WindowsRuntime
$null = [Windows.Storage.StorageFile, Windows.Storage, ContentType=WindowsRuntime]
$null = [Windows.Storage.FileAccessMode, Windows.Storage, ContentType=WindowsRuntime]
$null = [Windows.Storage.Streams.IRandomAccessStream, Windows.Storage.Streams, ContentType=WindowsRuntime]
$null = [Windows.Graphics.Imaging.BitmapDecoder, Windows.Graphics.Imaging, ContentType=WindowsRuntime]
$null = [Windows.Graphics.Imaging.SoftwareBitmap, Windows.Graphics.Imaging, ContentType=WindowsRuntime]
$null = [Windows.Media.Ocr.OcrEngine, Windows.Foundation, ContentType=WindowsRuntime]
$null = [Windows.Media.Ocr.OcrResult, Windows.Foundation, ContentType=WindowsRuntime]

function Await-WinRT($Operation, [Type]$ResultType) {
  $method = [System.WindowsRuntimeSystemExtensions].GetMethods() |
    Where-Object { $_.Name -eq 'AsTask' -and $_.IsGenericMethod -and $_.GetParameters().Count -eq 1 } |
    Select-Object -First 1
  $task = $method.MakeGenericMethod($ResultType).Invoke($null, @($Operation))
  $task.Wait()
  return $task.Result
}

$file = Await-WinRT ([Windows.Storage.StorageFile]::GetFileFromPathAsync($env:CODINGTO_OCR_IMAGE)) ([Windows.Storage.StorageFile])
$stream = Await-WinRT ($file.OpenAsync([Windows.Storage.FileAccessMode]::Read)) ([Windows.Storage.Streams.IRandomAccessStream])
$decoder = Await-WinRT ([Windows.Graphics.Imaging.BitmapDecoder]::CreateAsync($stream)) ([Windows.Graphics.Imaging.BitmapDecoder])
$bitmap = Await-WinRT ($decoder.GetSoftwareBitmapAsync()) ([Windows.Graphics.Imaging.SoftwareBitmap])
$engine = [Windows.Media.Ocr.OcrEngine]::TryCreateFromUserProfileLanguages()
if ($null -eq $engine) { throw 'No Windows OCR language is installed for the current user.' }
$result = Await-WinRT ($engine.RecognizeAsync($bitmap)) ([Windows.Media.Ocr.OcrResult])
[Console]::Out.Write($result.Text)
`
