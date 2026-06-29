package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"assetreuploader/internal/accounts"
	"assetreuploader/internal/download"
	"assetreuploader/internal/opencloud"
	"assetreuploader/internal/server"
)

//go:embed index.html
var indexHTML []byte

//go:embed icon.png
var iconPNG []byte

//go:embed faq/*.png
var faqFS embed.FS

//go:embed NexusReuploader.rbxmx
var pluginRBXMX []byte

const defaultKnownPlacesURL = "https://nexus-known-places.chatjust984.workers.dev"

func main() {
	enableANSIColors()
	cfg := loadConfig("config.ini")

	keyPath := cfg["apikey_file"]
	if keyPath == "" {
		keyPath = "apikey.txt"
	}
	cookiePath := cfg["cookie_file"]
	if cookiePath == "" {
		cookiePath = "cookie.txt"
	}
	accountsPath := cfg["accounts_file"]
	if accountsPath == "" {
		accountsPath = "accounts.json"
	}
	port := 38073
	if p, ok := cfg["port"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			port = n
		}
	}

	kpURL := cfg["knownplaces_url"]
	if strings.TrimSpace(kpURL) == "" {
		kpURL = defaultKnownPlacesURL
	}

	up := opencloud.New()
	dl := download.New()
	store := accounts.Load(accountsPath)
	srv := server.New(up, dl, store, keyPath, cookiePath, kpURL, cfg["knownplaces_key"])
	if n, err := strconv.Atoi(strings.TrimSpace(cfg["upload_speed"])); err == nil {
		srv.SetUploadSpeed(n)
	}
	connToken := loadOrCreateSecret("connector.secret")
	srv.SetConnectorToken(connToken)

	mux := srv.Routes()
	registerDiscordAuth(mux, port)
	mux.Handle("/faq/", http.FileServer(http.FS(faqFS)))
	mux.HandleFunc("/install-plugin", func(w http.ResponseWriter, r *http.Request) {
		dir := robloxPluginsDir()
		if dir == "" {
			http.Error(w, "could not locate your Roblox Plugins folder", http.StatusInternalServerError)
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		p := filepath.Join(dir, "NexusReuploader.rbxmx")
		out := bytes.ReplaceAll(pluginRBXMX, []byte("NEXUS_CONNECTOR_TOKEN"), []byte(connToken))
		if err := os.WriteFile(p, out, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"path":%q}`, p)
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(iconPNG)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	running := alreadyRunning(url)
	if !running {
		go func() { _ = srv.Start(port, originGuard(mux, port)) }()
		for i := 0; i < 60; i++ {
			if alreadyRunning(url) {
				break
			}
			time.Sleep(80 * time.Millisecond)
		}
	}
	if !openWindow(url, "Nexus Reuploader") {
		openBrowser(url)
		if !running {
			select {}
		}
	}
}

func alreadyRunning(url string) bool {
	c := &http.Client{Timeout: 600 * time.Millisecond}
	resp, err := c.Get(url + "/ping")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func originGuard(next http.Handler, port int) http.Handler {
	self1 := fmt.Sprintf("http://127.0.0.1:%d", port)
	self2 := fmt.Sprintf("http://localhost:%d", port)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			o := r.Header.Get("Origin")
			if o != "" && o != self1 && o != self2 {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func robloxPluginsDir() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Roblox", "Plugins")
	}
	la := os.Getenv("LOCALAPPDATA")
	if la == "" {
		return ""
	}
	return filepath.Join(la, "Roblox", "Plugins")
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "linux":
		_ = exec.Command("xdg-open", url).Start()
	default:
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
}

func loadOrCreateSecret(path string) string {
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	s := hex.EncodeToString(buf)
	_ = os.WriteFile(path, []byte(s), 0o600)
	return s
}

func loadConfig(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}
