package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

const discordClientID = "1520552771118694651"
const discordInviteURL = "https://discord.gg/j4NPfDwCtA"

type discordUser struct {
	LoggedIn  bool   `json:"loggedIn"`
	ID        string `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatarUrl"`
}

var (
	sessMu  sync.Mutex
	session discordUser
)

const sessionFile = "session.json"

func loadStoredSession() {
	b, err := os.ReadFile(sessionFile)
	if err != nil {
		return
	}
	var s discordUser
	if json.Unmarshal(b, &s) == nil && s.LoggedIn {
		sessMu.Lock()
		session = s
		sessMu.Unlock()
	}
}

func saveSession(s discordUser) {
	if b, err := json.Marshal(s); err == nil {
		_ = os.WriteFile(sessionFile, b, 0600)
	}
}

func clearStoredSession() { _ = os.Remove(sessionFile) }

// discordSecret returns the OAuth client secret ONLY from the environment.
// The shipped binary never carries a secret or a path to one; on players'
// machines this is empty and local token-exchange is disabled (sign-in is
// expected to route through the backend). Our dev box / server set the env.
func discordSecret() string {
	if s := strings.TrimSpace(os.Getenv("NEXUS_DISCORD_SECRET")); s != "" {
		return s
	}
	if p := strings.TrimSpace(os.Getenv("NEXUS_DISCORD_SECRET_FILE")); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

func redirectURI(port int) string { return fmt.Sprintf("http://127.0.0.1:%d/discord/callback", port) }

func authorizeURLApp(port int) string {
	p := url.Values{}
	p.Set("client_id", discordClientID)
	p.Set("redirect_uri", redirectURI(port))
	p.Set("response_type", "code")
	p.Set("scope", "identify")
	return "https://discord.com/oauth2/authorize?" + p.Encode()
}

func registerDiscordAuth(mux *http.ServeMux, port int) {
	loadStoredSession()
	mux.HandleFunc("/discord/login", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, authorizeURLApp(port), http.StatusFound)
	})

	mux.HandleFunc("/discord/open", func(w http.ResponseWriter, r *http.Request) {
		openBrowser(authorizeURLApp(port))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	mux.HandleFunc("/discord/invite", func(w http.ResponseWriter, r *http.Request) {
		openBrowser(discordInviteURL)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	mux.HandleFunc("/discord/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		secret := discordSecret()
		if code == "" || secret == "" {
			http.Error(w, "Discord sign-in is not configured yet (missing client secret).", http.StatusBadRequest)
			return
		}
		form := url.Values{}
		form.Set("client_id", discordClientID)
		form.Set("client_secret", secret)
		form.Set("grant_type", "authorization_code")
		form.Set("code", code)
		form.Set("redirect_uri", redirectURI(port))
		resp, err := http.PostForm("https://discord.com/api/v10/oauth2/token", form)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(b, &tok)
		if tok.AccessToken == "" {
			http.Error(w, "token exchange failed", http.StatusInternalServerError)
			return
		}
		req, _ := http.NewRequest("GET", "https://discord.com/api/v10/users/@me", nil)
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		ur, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ub, _ := io.ReadAll(ur.Body)
		ur.Body.Close()
		var u struct {
			ID         string `json:"id"`
			Username   string `json:"username"`
			Avatar     string `json:"avatar"`
			GlobalName string `json:"global_name"`
		}
		_ = json.Unmarshal(ub, &u)
		name := u.GlobalName
		if name == "" {
			name = u.Username
		}
		avatar := ""
		if u.Avatar != "" {
			avatar = fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.png?size=128", u.ID, u.Avatar)
		}
		sessMu.Lock()
		session = discordUser{LoggedIn: true, ID: u.ID, Username: name, AvatarURL: avatar}
		saveSession(session)
		sessMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body style='background:#0a0a0b;color:#fafafa;font-family:Segoe UI,sans-serif;text-align:center;padding:64px'><h1 style='color:#8b5cf6'>Signed in</h1><p>Signed in as <b>" + name + "</b>. Close this tab and return to the Nexus app.</p></body></html>"))
	})

	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		sessMu.Lock()
		s := session
		sessMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"loggedIn":      s.LoggedIn,
			"id":            s.ID,
			"username":      s.Username,
			"avatarUrl":     s.AvatarURL,
			"signinEnabled": discordSecret() != "",
		})
	})

	mux.HandleFunc("/discord/logout", func(w http.ResponseWriter, r *http.Request) {
		sessMu.Lock()
		session = discordUser{}
		clearStoredSession()
		sessMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
}
