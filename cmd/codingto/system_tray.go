package main

import (
	"io/fs"

	"codingto/internal/applog"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type systemTrayLabels struct {
	show string
	quit string
}

func labelsForSystemTray(language string) systemTrayLabels {
	if language == "en-US" {
		return systemTrayLabels{
			show: "Show CodingTo",
			quit: "Quit CodingTo",
		}
	}
	return systemTrayLabels{
		show: "打开 CodingTo",
		quit: "退出 CodingTo",
	}
}

func setupSystemTray(
	desktopApp *application.App,
	mainWindow *application.WebviewWindow,
	language string,
	quit func(),
) {
	labels := labelsForSystemTray(language)
	showWindow := func() {
		if mainWindow.IsMinimised() {
			mainWindow.UnMinimise()
		}
		mainWindow.Show().Focus()
	}

	menu := desktopApp.Menu.New()
	menu.Add(labels.show).OnClick(func(_ *application.Context) {
		showWindow()
	})
	menu.AddSeparator()
	menu.Add(labels.quit).OnClick(func(_ *application.Context) {
		quit()
	})

	tray := desktopApp.SystemTray.New()
	tray.SetTooltip("CodingTo")
	tray.OnClick(showWindow)
	tray.SetMenu(menu)
	if icon := systemTrayIcon(); len(icon) > 0 {
		tray.SetIcon(icon)
	}
}

func systemTrayIcon() []byte {
	matches, err := fs.Glob(Assets, "frontend/dist/assets/logo-*.png")
	if err != nil {
		applog.Errorf("find system tray icon: %v", err)
		return nil
	}
	if len(matches) == 0 {
		applog.Infof("system tray icon not found; using platform default")
		return nil
	}
	icon, err := Assets.ReadFile(matches[0])
	if err != nil {
		applog.Errorf("read system tray icon: %v", err)
		return nil
	}
	return icon
}
