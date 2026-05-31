package main

import (
	"embed"
	"log/slog"
	"os"
	"runtime/debug"

	"movielist-app/internal/utils"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Ініціалізуємо логування при старті
	utils.InitLogger()
	defer utils.CloseLogger()

	// 2. 🛡️ ГЛОБАЛЬНИЙ ПЕРЕХОПЛЮВАЧ КРАХІВ (ПАНІК)
	defer func() {
		if r := recover(); r != nil {
			// Якщо програма жорстко падає, ми записуємо це у наш лог-файл
			slog.Error("unhandled_panic",
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())),
			)
			os.Exit(1)
		}
	}()

	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "MovieList 2.0",
		Width:     1100,
		Height:    650,
		MinWidth:  860,
		MinHeight: 540,
		Frameless: true, // ⬅️ ОСЬ ГОЛОВНА МАГІЯ
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 30, G: 31, B: 34, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		// Якщо крах стався на рівні самого ядра Wails
		slog.Error("CRITICAL_WAILS_ERROR", slog.Any("error", err))
		os.Exit(1)
	}
}
