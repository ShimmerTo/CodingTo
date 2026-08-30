package main

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"codingto/internal/app"
	"codingto/internal/applog"
	"codingto/internal/browsersession"
	"codingto/internal/browserworkflow"
	"codingto/internal/documentbridge/bridge"
	sshbridge "codingto/internal/sshsecuritybridge/bridge"
	"codingto/internal/subagentbridge"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "document-bridge":
			os.Exit(bridge.Run(os.Args[2:]))
		case "subagent-bridge":
			os.Exit(subagentbridge.Run(os.Args[2:]))
		case "ssh-security-bridge":
			os.Exit(sshbridge.Run(os.Args[2:]))
		case "credential-provider":
			// Resolve the global browser profile directory so the credential
			// provider can locate the shared profile credentials.
			base, err := browserworkflow.ProfileBaseDir()
			if err != nil {
				applog.Infof("credential provider: %v", err)
				os.Exit(1)
			}
			if err := browserworkflow.RunCredentialProvider(os.Stdin, os.Stdout, base); err != nil {
				os.Exit(1)
			}
			return
		}
	}
	// 桌面客户端：把关键日志（含启动失败）写入 ~/.codingto/logs/codingto/年/月/日.log。
	// Init 会把标准库 log 输出重定向到该文件，因此后面的 log.Fatal 报错也会落盘。
	if err := applog.Init(); err != nil {
		applog.Infof("init file logging: %v", err)
	}

	// 图形界面依赖系统 webview 运行时（Windows 为 WebView2）。缺失时窗口无法渲染，
	// 因此在创建窗口前用原生对话框提示用户前往下载安装，确认后退出由用户安装后重开。
	requireWebView()

	// 桌面客户端单实例：多个实例会同时启动管家渠道的长连接，互相踢线并
	// 互相覆盖渠道连接状态（导致已连接却显示黄色/橙色），因此同一时刻只允许一个实例。
	// 已有关闭残留实例后重新启动即可（锁文件残留时自动回收）。
	release, err := acquireSingleInstance()
	if err != nil {
		log.Fatalf("CodingTo: %v，请先关闭已运行的实例后再启动", err)
	}
	defer release()

	browserSessions, err := browsersession.New(browsersession.Options{})
	if err != nil {
		log.Fatal(err)
	}
	appService, err := app.NewApp(browserSessions)
	if err != nil {
		log.Fatal(err)
	}
	notificationService := notifications.New()

	app := application.New(application.Options{
		Name:        "CodingTo",
		Description: "A focused local-first desktop workspace for Pi Agent.",
		Services: []application.Service{
			application.NewService(appService),
			application.NewService(notificationService),
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
		Width:            1440,
		Height:           860,
		MinWidth:         1080,
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
	// 退出流程。旧实现直接 app.Quit()，但 wails v3 在 Windows 上是"先同步执行
	// OnShutdown/ServiceShutdown、再销毁窗口"：ServiceShutdown 中的 steward
	// 渠道关闭等操作最多可阻塞 3 秒，全部跑在 UI 主线程，导致点关闭后界面
	// 卡 3 秒没反应。这里改为：
	//   1) 立即向前端发送 app:shutting-down 事件，显示"正在关闭中"蒙层；
	//   2) ServiceShutdown 放进后台 goroutine 执行，不阻塞 UI 主线程；
	//   3) 清理完成后释放单实例锁、关闭日志并退出进程。
	var shutdownStarted atomic.Bool
	shutdownDone := make(chan struct{})
	var shutdownOnce sync.Once
	beginShutdown := func() {
		shutdownOnce.Do(func() {
			shutdownStarted.Store(true)
			application.Get().Event.Emit("app:shutting-down", map[string]any{})
			go func() {
				if err := appService.ServiceShutdown(); err != nil {
					applog.Errorf("service shutdown: %v", err)
				}
				applog.Close()
				release()
				close(shutdownDone)
				os.Exit(0)
			}()
		})
	}

	// Closing the main window keeps CodingTo and its background services alive.
	// RegisterHook runs before Wails' built-in close listener, so cancelling here
	// preserves the window for restoration from the system tray.
	mainWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if shutdownStarted.Load() {
			return
		}
		mainWindow.Hide()
		event.Cancel()
	})
	// Cleanup for platform-initiated termination. Tray quit uses beginShutdown;
	// ServiceShutdown is idempotent if both paths are reached.
	app.OnShutdown(func() {
		if err := appService.ServiceShutdown(); err != nil {
			applog.Errorf("service shutdown: %v", err)
		}
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
	mainWindow.OnWindowEvent(events.Common.WindowMaximise, func(event *application.WindowEvent) {
		application.Get().Event.Emit("window:maximised")
	})
	mainWindow.OnWindowEvent(events.Common.WindowUnMaximise, func(event *application.WindowEvent) {
		application.Get().Event.Emit("window:unmaximised")
	})
	appService.SetWindow(mainWindow)
	setupSystemTray(app, mainWindow, appService.GetBootstrap().Config.Preferences.Language, beginShutdown)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
	// 窗口被外部销毁时 Run 才会返回；若退出流程已经启动，等待后台清理
	// 完成，避免 main 提前返回中断尚未结束的清理 goroutine。
	if shutdownStarted.Load() {
		select {
		case <-shutdownDone:
		case <-time.After(15 * time.Second):
		}
	}
}
