package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:assets
var assets embed.FS

func main() {
	runtimeApp, err := NewRuntimeApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app := application.New(application.Options{
		Name:        "deskpatrol",
		Description: "DeskPatrol 设备客户端",
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.deskpatrol.client",
			ExitCode: 0,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				runtimeApp.ShowWindow()
			},
		},
		Assets: application.AssetOptions{Handler: application.BundledAssetFileServer(assets), DisableLogging: true},
	})
	runtimeApp.SetApplication(app)
	app.RegisterService(application.NewService(runtimeApp))
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "main", Title: "DeskPatrol", Width: 920, Height: 650, MinWidth: 760, MinHeight: 560,
		BackgroundColour: application.NewRGB(244, 246, 245), UseApplicationMenu: false,
	})
	runtimeApp.SetWindow(window)
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
