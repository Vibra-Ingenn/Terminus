package main

import (
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"terminus/cmd/docparse/webui"
)

// Tool card (tool-card/0.1): how Janus, Vibe Engine and other tools find Terminus.
const (
	toolID      = "terminus"
	toolVersion = "1.0.0"
)

// toolCardJSON is the card shipped in the web UI folder, served at /tool.json.
func toolCardJSON() []byte {
	data, err := webui.Static.ReadFile("static/tool.json")
	if err != nil {
		return []byte(`{"spec":"tool-card/0.1","id":"terminus"}`)
	}
	return data
}

// registryDir is the shared folder where running tools sign in.
func registryDir() string {
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "AdeptusTools", "registry")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "adeptus-tools", "registry")
}

// registerTool writes this tool's card plus its address to the registry and returns
// a function that removes the entry. Failing to register never stops Terminus.
func registerTool(address string) func() {
	dir := registryDir()
	if dir == "" {
		return func() {}
	}
	var card map[string]any
	if err := json.Unmarshal(toolCardJSON(), &card); err != nil {
		return func() {}
	}
	card["address"] = address
	card["pid"] = os.Getpid()
	card["started_at"] = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return func() {}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return func() {}
	}
	entry := filepath.Join(dir, toolID+".json")
	if err := os.WriteFile(entry, data, 0644); err != nil {
		return func() {}
	}

	var once sync.Once
	cleanup := func() { once.Do(func() { _ = os.Remove(entry) }) }

	// Remove the entry on Ctrl+C or a normal stop, too.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		cleanup()
		os.Exit(0)
	}()
	return cleanup
}
