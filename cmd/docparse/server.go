package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"terminus/cmd/docparse/webui"
	"terminus/internal/tools"
)

type serveConfig struct {
	port     int
	packName string
	openUI   bool
}

func configureHTTPServer(cfg serveConfig) *http.ServeMux {
	reg := tools.NewRegistry()
	mux := http.NewServeMux()

	staticRoot, _ := fs.Sub(webui.Static, "static")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(webui.Static, "static/index.html")
		if err != nil {
			http.Error(w, "missing UI", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticRoot))))

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"service":"terminus"}`))
	})

	uploadRoot := filepath.Join(defaultOutputDir(), "api-uploads")

	mux.HandleFunc("/v1/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		path := strings.TrimSpace(r.URL.Query().Get("path"))
		abs, err := safeUploadPath(uploadRoot, path)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		ext := strings.ToLower(filepath.Ext(abs))
		switch ext {
		case ".png":
			w.Header().Set("Content-Type", "image/png")
		case ".jpg", ".jpeg":
			w.Header().Set("Content-Type", "image/jpeg")
		case ".gif":
			w.Header().Set("Content-Type", "image/gif")
		case ".webp":
			w.Header().Set("Content-Type", "image/webp")
		case ".pdf":
			w.Header().Set("Content-Type", "application/pdf")
		default:
			http.Error(w, "unsupported preview type", http.StatusUnsupportedMediaType)
			return
		}
		http.ServeFile(w, r, abs)
	})

	mux.HandleFunc("/v1/parse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		path, includeRaw, err := readPathFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pack := r.URL.Query().Get("pack")
		if pack == "" {
			pack = cfg.packName
		}
		if pack == "" {
			pack = os.Getenv("TERMINUS_PACKS")
		}
		res := reg.Dispatch(tools.ToolCall{
			Name: "document_parse",
			Arguments: map[string]any{
				"path":        path,
				"include_raw": boolString(includeRaw),
				"pack":        pack,
			},
		})
		if !res.Success {
			writeToolResult(w, res)
			return
		}
		enriched, err := enrichParseResponse(res.Output, path, uploadRoot)
		if err != nil {
			writeToolResult(w, res)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(enriched)
	})

	mux.HandleFunc("/v1/heal", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		path, _, err := readPathFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		outDir := r.URL.Query().Get("out_dir")
		if outDir == "" {
			outDir = defaultOutputDir()
		}
		report := tools.HealDocument(path, outDir)
		w.Header().Set("Content-Type", "application/json")
		code := http.StatusOK
		if !report.Succeeded {
			code = http.StatusUnprocessableEntity
		}
		w.WriteHeader(code)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	})

	return mux
}

func parseServeFlags(args []string) (serveConfig, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfg := serveConfig{port: 8765}
	fs.IntVar(&cfg.port, "port", cfg.port, "listen port")
	fs.StringVar(&cfg.packName, "pack", os.Getenv("TERMINUS_PACKS"), "field pack to enable (e.g. medical)")
	if err := fs.Parse(args); err != nil {
		return serveConfig{}, err
	}
	return cfg, nil
}

func runServe(args []string) int {
	cfg, err := parseServeFlags(args)
	if err != nil {
		return 2
	}
	return listenAndServe(cfg)
}

func runUI(args []string) int {
	cfg, err := parseServeFlags(args)
	if err != nil {
		return 2
	}
	cfg.openUI = true
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.port)
	go openBrowser("http://" + addr + "/")
	return listenAndServe(cfg)
}

func listenAndServe(cfg serveConfig) int {
	mux := configureHTTPServer(cfg)
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.port)
	if cfg.openUI {
		fmt.Fprintf(os.Stderr, "Terminus is open at http://%s/\n", addr)
	} else {
		fmt.Fprintf(os.Stderr, "Terminus listening on http://%s/\n", addr)
	}
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}

func openBrowser(openURL string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", openURL).Start()
	case "darwin":
		_ = exec.Command("open", openURL).Start()
	default:
		_ = exec.Command("xdg-open", openURL).Start()
	}
}

func enrichParseResponse(jsonOut string, path string, uploadRoot string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		return nil, err
	}
	doc["path"] = path
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		doc["preview_kind"] = "image"
		doc["preview_url"] = "/v1/preview?path=" + url.QueryEscape(path)
	default:
		doc["preview_kind"] = "text"
	}
	_ = uploadRoot
	return json.Marshal(doc)
}

func safeUploadPath(uploadRoot, path string) (string, error) {
	absUpload, err := filepath.Abs(uploadRoot)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absUpload, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("outside upload dir")
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", err
	}
	return absPath, nil
}
