// Command bindingruntimeprobe runs a minimal real Wails desktop transport around
// the production FileService. It is an integration harness, not a mock service.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func main() {
	workspace := flag.String("workspace", "", "absolute fixture workspace path")
	assets := flag.String("assets", "", "absolute probe asset directory")
	flag.Parse()

	root, err := filepath.Abs(*workspace)
	if err != nil || *workspace == "" {
		fatalf("invalid workspace: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		fatalf("workspace is not a directory: %s", root)
	}
	assetRoot, err := filepath.Abs(*assets)
	if err != nil || *assets == "" {
		fatalf("invalid asset directory: %v", err)
	}
	assetInfo, err := os.Stat(assetRoot)
	if err != nil || !assetInfo.IsDir() {
		fatalf("asset root is not a directory: %s", assetRoot)
	}

	fileService := services.NewFileService()
	if err := services.SetFileServiceWorkspaceRoot(fileService, root); err != nil {
		fatalf("set trusted fixture workspace: %v", err)
	}

	app := application.New(application.Options{
		Name:     "koyori-ide-binding-runtime-probe",
		Assets:   application.AssetOptions{Handler: http.FileServer(http.Dir(assetRoot))},
		Services: []application.Service{application.NewService(fileService)},
	})
	services.AttachFileService(fileService, app)
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "binding-runtime-probe",
		Title:  "Koyori IDE binding runtime probe",
		Width:  480,
		Height: 320,
		URL:    "/",
		Hidden: true,
	})
	if err := app.Run(); err != nil {
		fatalf("run Wails desktop transport: %v", err)
	}
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
