package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"assetreuploader/internal/accounts"
	"assetreuploader/internal/download"
	"assetreuploader/internal/opencloud"
	"assetreuploader/internal/roblox"
	"assetreuploader/internal/spoofer"
)

const (
	cReset  = "\x1b[0m"
	cGreen  = "\x1b[32m"
	cRed    = "\x1b[31m"
	cCyan   = "\x1b[36m"
	cYellow = "\x1b[33m"
)

type item struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	DataB64 string `json:"dataB64"`
}
type reuploadRequest struct {
	CreatorID  string `json:"creatorId"`
	IsGroup    bool   `json:"isGroup"`
	UniverseID string `json:"universeId"`
	AssetType  string `json:"assetType"`
	PlaceID    string `json:"placeId"`
	Items      []item `json:"items"`
}
type reuploadResponse struct {
	Mapping      map[string]string `json:"mapping"`
	Errors       map[string]string `json:"errors"`
	QuotaReached string            `json:"quotaReached,omitempty"`
	Canceled     bool              `json:"canceled,omitempty"`
	AuthFailed   bool              `json:"authFailed,omitempty"`
}

type Activity struct {
	Time string `json:"time"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type Server struct {
	up         *opencloud.Uploader
	dl         *download.Downloader
	sp         *spoofer.Resolver
	store       *accounts.Store
	keyPath        string
	cookiePath     string
	uploadSpeed    int
	connectorToken string

	mu                 sync.Mutex
	feed               []Activity
	busy               bool
	lastStat           string
	pending            *job
	pluginPing         time.Time
	pluginWasConnected bool
	canceled           bool
	target             targetPref
}

type targetPref struct {
	CreatorID string `json:"creatorId"`
	IsGroup   bool   `json:"isGroup"`
}

const targetFile = "target.json"

type job struct {
	AssetType string `json:"assetType"`
	SkipOwned bool   `json:"skipOwned"`
	CreatorID string `json:"creatorId"`
	IsGroup   bool   `json:"isGroup"`
}

func New(up *opencloud.Uploader, dl *download.Downloader, store *accounts.Store, keyPath, cookiePath, kpURL, kpKey string) *Server {
	sp := spoofer.New()
	if strings.TrimSpace(kpURL) != "" {
		sp = spoofer.NewWithBackend(kpURL, kpKey)
	}
	s := &Server{up: up, dl: dl, sp: sp, store: store, keyPath: keyPath, cookiePath: cookiePath}
	if b, err := os.ReadFile(targetFile); err == nil {
		_ = json.Unmarshal(b, &s.target)
	}
	return s
}

func (s *Server) SetUploadSpeed(n int) {
	if n > 0 {
		s.uploadSpeed = n
	}
}

func (s *Server) SetConnectorToken(t string) { s.connectorToken = strings.TrimSpace(t) }

// connectorAuthed verifies a request came from the real Nexus plugin. The plugin
// sends X-Nexus-Token (injected at install). Empty token = guard disabled.
func (s *Server) connectorAuthed(r *http.Request) bool {
	return s.connectorToken == "" || r.Header.Get("X-Nexus-Token") == s.connectorToken
}

func (s *Server) push(kind, text string) {
	s.mu.Lock()
	s.feed = append(s.feed, Activity{Time: time.Now().Format("15:04:05"), Kind: kind, Text: text})
	if len(s.feed) > 400 {
		s.feed = s.feed[len(s.feed)-400:]
	}
	s.mu.Unlock()
}

const secretMagic = "NXS1"

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	if len(b) >= 4 && string(b[:4]) == secretMagic {
		if dec, ok := secretOpen(b[4:]); ok {
			return strings.TrimSpace(string(dec))
		}
		return strings.TrimSpace(string(b[4:]))
	}
	return strings.TrimSpace(string(b))
}
func fileHasContent(p string) bool { return readTrim(p) != "" }

// writeSecret stores a credential encrypted at rest (DPAPI on Windows),
// tagged with a magic prefix so reads can tell sealed from legacy plaintext.
func writeSecret(p, val string) error {
	data := append([]byte(secretMagic), secretSeal([]byte(strings.TrimSpace(val)))...)
	return os.WriteFile(p, data, 0o600)
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/reupload", s.handleReupload)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/config", s.handleConfig)
	mux.HandleFunc("/whoami", s.handleWhoAmI)
	mux.HandleFunc("/accounts", s.handleAccounts)
	mux.HandleFunc("/accounts/add", s.handleAccountAdd)
	mux.HandleFunc("/accounts/remove", s.handleAccountRemove)
	mux.HandleFunc("/job", s.handleJob)
	mux.HandleFunc("/plugin-log", s.handlePluginLog)
	mux.HandleFunc("/cancel", s.handleCancel)
	mux.HandleFunc("/target", s.handleTarget)
	return mux
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	wasBusy := s.busy
	s.canceled = true
	s.mu.Unlock()
	if wasBusy {
		s.push("info", "Stop requested - finishing the current item, then halting.")
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var t targetPref
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t.CreatorID = strings.TrimSpace(t.CreatorID)
	s.mu.Lock()
	s.target = t
	s.mu.Unlock()
	b, _ := json.Marshal(t)
	_ = os.WriteFile(targetFile, b, 0o600)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var j job
		if err := json.NewDecoder(r.Body).Decode(&j); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.pending = &j
		s.canceled = false
		s.mu.Unlock()
		s.push("info", "Requested: reupload "+j.AssetType)
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if !s.connectorAuthed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	reconnected := !s.pluginWasConnected
	s.pluginWasConnected = true
	s.pluginPing = time.Now()
	j := s.pending
	s.pending = nil
	s.mu.Unlock()
	if reconnected {
		s.push("info", "Plugin connected.")
	}
	if j == nil {
		writeJSON(w, map[string]any{"assetType": ""})
		return
	}
	writeJSON(w, j)
}

func (s *Server) handlePluginLog(w http.ResponseWriter, r *http.Request) {
	if !s.connectorAuthed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var b struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if b.Kind == "" {
		b.Kind = "info"
	}
	s.push(b.Kind, b.Text)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	feed := make([]Activity, len(s.feed))
	copy(feed, s.feed)
	busy, last := s.busy, s.lastStat
	pluginConnected := time.Since(s.pluginPing) < 8*time.Second
	disconnected := s.pluginWasConnected && !pluginConnected
	if disconnected {
		s.pluginWasConnected = false
	}
	tgt := s.target
	s.mu.Unlock()
	if disconnected {
		s.push("info", "Plugin disconnected.")
	}
	writeJSON(w, map[string]any{
		"hasKey":          fileHasContent(s.keyPath),
		"hasCookie":       fileHasContent(s.cookiePath),
		"accounts":        s.store.Count(),
		"busy":            busy,
		"last":            last,
		"pluginConnected": pluginConnected,
		"creatorId":       tgt.CreatorID,
		"isGroup":         tgt.IsGroup,
		"feed":            feed,
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ApiKey      *string `json:"apiKey"`
		Cookie      *string `json:"cookie"`
		ClearKey    bool    `json:"clearKey"`
		ClearCookie bool    `json:"clearCookie"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.ClearKey {
		_ = os.Remove(s.keyPath)
		s.push("info", "API key cleared.")
	} else if body.ApiKey != nil && strings.TrimSpace(*body.ApiKey) != "" {
		_ = writeSecret(s.keyPath, *body.ApiKey)
		s.push("info", "API key saved.")
	}
	if body.ClearCookie {
		_ = os.Remove(s.cookiePath)
		s.push("info", "Cookie cleared.")
	} else if body.Cookie != nil && strings.TrimSpace(*body.Cookie) != "" {
		_ = writeSecret(s.cookiePath, *body.Cookie)
		s.push("info", "Cookie saved.")
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	cookie := readTrim(s.cookiePath)
	if cookie == "" {
		writeJSON(w, map[string]any{"connected": false})
		return
	}
	p, err := roblox.Authenticated(cookie)
	if err != nil {
		writeJSON(w, map[string]any{"connected": false, "error": "cookie invalid or expired"})
		return
	}
	writeJSON(w, map[string]any{"connected": true, "id": p.Id, "name": p.Name, "displayName": p.DisplayName, "avatarUrl": p.AvatarUrl})
}

type accountView struct {
	Index       int    `json:"index"`
	Name        string `json:"name"`
	CreatorID   string `json:"creatorId"`
	IsGroup     bool   `json:"isGroup"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	AvatarUrl   string `json:"avatarUrl"`
	HasCookie   bool   `json:"hasCookie"`
	KeyTail     string `json:"keyTail"`
	Role        string `json:"role"`
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	list := s.store.All()
	views := make([]accountView, 0, len(list))
	for i, a := range list {
		role := "downloader"
		if a.APIKey != "" {
			role = "uploader"
		}
		v := accountView{Index: i, Name: a.Name, CreatorID: a.CreatorID, IsGroup: a.IsGroup, HasCookie: a.Cookie != "", Role: role}
		if len(a.APIKey) >= 4 {
			v.KeyTail = a.APIKey[len(a.APIKey)-4:]
		}
		if a.APIKey != "" && a.CreatorID != "" {
			if info, err := roblox.Resolve(a.CreatorID, a.IsGroup); err == nil {
				v.Username = info.Name
				v.DisplayName = info.DisplayName
			}
			v.AvatarUrl = roblox.AvatarURL(a.CreatorID, a.IsGroup)
		} else if a.Cookie != "" {
			if p, err := roblox.Authenticated(a.Cookie); err == nil {
				v.Username = p.Name
				v.DisplayName = p.DisplayName
				v.AvatarUrl = p.AvatarUrl
			}
		}
		views = append(views, v)
	}
	writeJSON(w, map[string]any{"accounts": views})
}

func (s *Server) handleAccountAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var a accounts.Account
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.Name = strings.TrimSpace(a.Name)
	a.APIKey = strings.TrimSpace(a.APIKey)
	a.CreatorID = strings.TrimSpace(a.CreatorID)
	a.Cookie = strings.TrimSpace(a.Cookie)
	if a.APIKey == "" && a.Cookie == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "add an API key (uploader) or a cookie (downloader)"})
		return
	}
	role := "downloader"
	if a.APIKey != "" {
		role = "uploader"
		if a.CreatorID == "" {
			writeJSON(w, map[string]any{"ok": false, "error": "an uploader needs a creator id"})
			return
		}
		info, err := roblox.Resolve(a.CreatorID, a.IsGroup)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "could not find that " + creatorKind(a.IsGroup) + " id"})
			return
		}
		if a.Name == "" {
			a.Name = info.DisplayName
		}
	} else if a.Name == "" {
		if p, perr := roblox.Authenticated(a.Cookie); perr == nil {
			a.Name = p.DisplayName
		} else {
			a.Name = "Downloader"
		}
	}
	if err := s.store.Add(a); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.push("info", "Account added: "+a.Name+" ("+role+")")
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAccountRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.Remove(body.Index); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func creatorKind(isGroup bool) string {
	if isGroup {
		return "group"
	}
	return "user"
}

// uploaders are the accounts that upload via Open Cloud (rotated on quota).
func (s *Server) uploaders(req reuploadRequest) []accounts.Account {
	if ups := s.store.Uploaders(); len(ups) > 0 {
		return ups
	}
	key := readTrim(s.keyPath)
	if key == "" || req.CreatorID == "" {
		return nil
	}
	return []accounts.Account{{
		Name: "primary", APIKey: key, CreatorID: req.CreatorID, IsGroup: req.IsGroup,
	}}
}

// downloaderCookies are the cookies used to fetch assets (rotated per asset).
// The primary cookie.txt is tried first, then any downloader accounts.
func (s *Server) downloaderCookies() []string {
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	add(readTrim(s.cookiePath))
	for _, c := range s.store.DownloaderCookies() {
		add(c)
	}
	return out
}

func (s *Server) handleReupload(w http.ResponseWriter, r *http.Request) {
	if !s.connectorAuthed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req reuploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.busy = false; s.mu.Unlock() }()

	resp := reuploadResponse{Mapping: map[string]string{}, Errors: map[string]string{}}
	pool := s.uploaders(req)
	if len(pool) == 0 {
		s.push("fail", "No uploader configured. Add an account with an API key in Connected Accounts, or set an API key.")
		writeJSON(w, resp)
		return
	}

	dlCookies := s.downloaderCookies()
	head := fmt.Sprintf("Reuploading %d %s across %d uploader(s), %d downloader(s)...", len(req.Items), req.AssetType, len(pool), len(dlCookies))
	log.Printf("%s%s%s", cCyan, head, cReset)
	s.push("info", head)

	s.prefetch(&req, dlCookies)

	workers := s.uploadSpeed
	if workers <= 0 {
		workers = 4
	}
	if workers > 16 {
		workers = 16
	}
	var mu sync.Mutex // guards next, curAcc, stopAll, ok, fail, and the resp maps
	next, curAcc, ok, fail := 0, 0, 0, 0
	stopAll := false
	isCanceled := func() bool { s.mu.Lock(); c := s.canceled; s.mu.Unlock(); return c }

	var wg sync.WaitGroup
	for wkr := 0; wkr < workers; wkr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if isCanceled() {
					return
				}
				mu.Lock()
				if stopAll || next >= len(req.Items) {
					mu.Unlock()
					return
				}
				it := req.Items[next]
				next++
				mu.Unlock()

				for { // retry the item across accounts on quota
					if isCanceled() {
						return
					}
					mu.Lock()
					a, stopped := curAcc, stopAll
					mu.Unlock()
					if stopped || a >= len(pool) {
						mu.Lock()
						if resp.Mapping[it.ID] == "" && resp.Errors[it.ID] == "" {
							resp.Errors[it.ID] = "all accounts at quota"
						}
						mu.Unlock()
						break
					}
					acc := pool[a]
					newID, err := s.uploadOne(acc, req.AssetType, it)
					if err == nil {
						mu.Lock()
						ok++
						resp.Mapping[it.ID] = newID
						mu.Unlock()
						s.push("ok", fmt.Sprintf("%s  ->  %s  [%s]", it.ID, newID, acc.Name))
						if req.UniverseID != "" && req.AssetType == "Audio" {
							if gerr := s.up.GrantUniverse(acc.APIKey, newID, req.UniverseID); gerr != nil {
								s.push("info", "permission note ("+newID+"): "+gerr.Error())
							} else {
								s.push("info", "granted game access to "+newID)
							}
						}
						break
					}
					var qe *opencloud.QuotaError
					if errors.As(err, &qe) {
						mu.Lock()
						rotated, exhausted := false, false
						if curAcc == a { // first worker to hit this account's quota rotates
							curAcc++
							rotated = true
							if curAcc >= len(pool) {
								stopAll = true
								exhausted = true
								resp.QuotaReached = qe.Error()
							}
						}
						mu.Unlock()
						if rotated {
							s.push("quota", acc.Name+": "+qe.Error())
							if exhausted {
								s.push("info", "All accounts exhausted. Add another in Connected Accounts to continue.")
							} else {
								s.push("info", "Rotating to next account.")
							}
						}
						continue // retry this item with the next account
					}
					var ae *opencloud.AuthError
					if errors.As(err, &ae) {
						msg := "Can't upload to " + creatorKind(acc.IsGroup) + " " + acc.CreatorID + ". This API key isn't authorized for that creator - make sure the key's creator matches and you have upload access."
						mu.Lock()
						stopAll = true
						resp.AuthFailed = true
						resp.Errors[it.ID] = msg
						fail++
						mu.Unlock()
						s.push("fail", msg)
						return
					}
					mu.Lock()
					fail++
					resp.Errors[it.ID] = err.Error()
					mu.Unlock()
					s.push("fail", it.ID+"  "+err.Error())
					break
				}
			}
		}()
	}
	wg.Wait()

	if isCanceled() {
		resp.Canceled = true
	}
	for _, it := range req.Items {
		if resp.Mapping[it.ID] == "" && resp.Errors[it.ID] == "" {
			switch {
			case resp.QuotaReached != "":
				resp.Errors[it.ID] = "all accounts at quota"
			case resp.Canceled:
				resp.Errors[it.ID] = "stopped by user"
			default:
				resp.Errors[it.ID] = "not processed"
			}
		}
	}

	summary := fmt.Sprintf("Done: %d reuploaded, %d failed.", ok, fail)
	if resp.QuotaReached != "" {
		summary = fmt.Sprintf("%d reuploaded, then all accounts hit quota.", ok)
	}
	s.push("info", summary)
	s.mu.Lock()
	s.lastStat = summary
	s.mu.Unlock()
	log.Printf("%s%s%s", cGreen, summary, cReset)
	writeJSON(w, resp)
}

func (s *Server) prefetch(req *reuploadRequest, cookies []string) {
	need := make([]string, 0, len(req.Items))
	idx := make(map[string]int, len(req.Items))
	for i := range req.Items {
		idx[req.Items[i].ID] = i
		if req.Items[i].DataB64 == "" {
			need = append(need, req.Items[i].ID)
		}
	}
	if len(need) == 0 || len(cookies) == 0 {
		return
	}
	got, _ := s.sp.Resolve(cookies, req.PlaceID, need)
	for id, data := range got {
		if i, ok := idx[id]; ok {
			req.Items[i].DataB64 = base64.StdEncoding.EncodeToString(data)
		}
	}
	if len(got) > 0 {
		s.push("info", fmt.Sprintf("Fetched %d/%d assets via place spoofing across %d downloader(s).", len(got), len(need), len(cookies)))
	}
}

func (s *Server) uploadOne(acc accounts.Account, assetType string, it item) (string, error) {
	var data []byte
	if it.DataB64 != "" {
		d, err := base64.StdEncoding.DecodeString(it.DataB64)
		if err != nil {
			return "", fmt.Errorf("bad payload: %w", err)
		}
		data = d
	} else {
		d, err := s.dl.Asset(acc.Cookie, it.ID)
		if err != nil {
			return "", fmt.Errorf("download: %w", err)
		}
		data = d
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty data")
	}
	name := it.Name
	if name == "" {
		name = "Reupload_" + it.ID
	}
	switch assetType {
	case "Animation", "Model", "Audio", "Decal":
		return s.up.Upload(acc.APIKey, assetType, data, name, acc.IsGroup, acc.CreatorID)
	default:
		return "", fmt.Errorf("asset type %q not supported", assetType)
	}
}

func (s *Server) Start(port int, h http.Handler) error {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	log.Printf("listening on http://%s", addr)
	return http.ListenAndServe(addr, h)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
