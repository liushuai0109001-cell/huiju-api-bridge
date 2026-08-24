package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

func main() {
	executable, err := os.Executable()
	if err != nil {
		panic(err)
	}
	appDir := filepath.Dir(executable)
	store, err := NewConfigStore(filepath.Join(appDir, "config.json"))
	if err != nil {
		panic(err)
	}
	logFile, err := os.OpenFile(filepath.Join(appDir, "bridge.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer logFile.Close()
	buffer := &LogBuffer{}
	logger := log.New(io.MultiWriter(logFile, buffer), "", log.LstdFlags)
	license, err := NewLicenseManager(appDir, licensePublicKey)
	if err != nil {
		panic(err)
	}
	bridge := NewBridge(store, logger, license)
	ui := NewDesktopUI(store, bridge, buffer, appDir, license)
	if err := ui.Run(); err != nil {
		panic(err)
	}
}
