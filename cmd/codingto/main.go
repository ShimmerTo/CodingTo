package main

import (
	"log"
	"os"
	"path/filepath"

	"codingto/internal/app"
	"codingto/internal/browsersession"
	"codingto/internal/browserworkflow"
	"codingto/internal/documentbridge/bridge"
	"codingto/internal/subagentbridge"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "document-bridge":
			os.Exit(bridge.Run(os.Args[2:]))
		case "subagent-bridge":
			os.Exit(subagentbridge.Run(os.Args[2:]))
		case "credential-provider":
			// Resolve the global browser profile directory so the credential
			// provider can locate the shared profile credentials.
			base, err := browserworkflow.ProfileBaseDir()
			if err != nil {
				log.Printf("credential provider: %v", err)
				os.Exit(1)
			}
			if err := browserworkflow.RunCredentialProvider(os.Stdin, os.Stdout, base); err != nil {
				os.Exit(1)
			}
			return
		}
	}
	browserSessions, err := browsersession.New(browsersession.Options{})
	if err != nil {
		log.Fatal(err)
	}
	appService, err := app.NewApp(browserSessions)
	if err != nil {
		log.Fatal(err)
	}

	app := application.New(application.Options{
		Name:        "CodingTo",
		Description: "A focused local-first desktop workspace for Pi Agent.",
		Services: []application.Service{
			application.NewService(appService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(Assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	mainWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "CodingTo",
		Width:            1360,
		Height:           860,
		MinWidth:         980,
		MinHeight:        680,
		Frameless:        true,
		EnableFileDrop:   true,
		BackgroundColour: application.NewRGB(248, 248, 247),
		URL:              "/",
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: false,
		},
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 42,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
	})
	mainWindow.OnWindowEvent(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		details := event.Context().DropTargetDetails()
		if details == nil || details.ElementID != "chat-attachment-drop-target" {
			return
		}
		files := make([]map[string]any, 0)
		for _, path := range event.Context().DroppedFiles() {
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			files = append(files, map[string]any{
				"path": path,
				"name": filepath.Base(path),
				"size": info.Size(),
			})
		}
		if len(files) > 0 {
			application.Get().Event.Emit("attachments:dropped", map[string]any{"files": files})
		}
	})
	appService.SetWindow(mainWindow)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
