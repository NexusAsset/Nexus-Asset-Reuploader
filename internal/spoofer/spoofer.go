package spoofer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	developBase = "https://develop.roblox.com/v1/assets?assetIds="
	batchURL    = "https://assetdelivery.roblox.com/v2/assets/batch"
	userAgent   = "RobloxStudio/WinInet"
)

var decalTexRe = regexp.MustCompile(`name="Texture">\s*<url>[^<]*?(\d{3,})`)

func decalImageID(b []byte) (string, bool) {
	if !bytes.Contains(b, []byte(`class="Decal"`)) {
		return "", false
	}
	m := decalTexRe.FindSubmatch(b)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

type Resolver struct {
	http        *http.Client
	sem         chan struct{}
	mu          sync.Mutex
	placesCache map[string][]string
	backendURL  string
	backendKey  string
}

func New() *Resolver {
	return &Resolver{
		http:        &http.Client{Timeout: 30 * time.Second},
		sem:         make(chan struct{}, 8),
		placesCache: map[string][]string{},
	}
}

func NewWithBackend(url, key string) *Resolver {
	r := New()
	r.backendURL = strings.TrimRight(strings.TrimSpace(url), "/")
	r.backendKey = strings.TrimSpace(key)
	return r
}

func (r *Resolver) acquire() { r.sem <- struct{}{} }
func (r *Resolver) release() { <-r.sem }

type loc struct {
	url string
	err string
}

func (r *Resolver) Resolve(cookie, currentPlaceID string, ids []string) (map[string][]byte, map[string]string) {
	out, errs, won := r.resolveOnce(cookie, currentPlaceID, ids)

	innerOf := map[string]string{}
	var inner []string
	for id, b := range out {
		if iid, ok := decalImageID(b); ok && iid != id {
			innerOf[id] = iid
			inner = append(inner, iid)
		}
	}
	if len(inner) > 0 {
		imgOut, imgErr, imgWon := r.resolveOnce(cookie, currentPlaceID, inner)
		for orig, iid := range innerOf {
			if ib, ok := imgOut[iid]; ok {
				out[orig] = ib
				delete(errs, orig)
			} else {
				delete(out, orig)
				errs[orig] = "decal image " + iid + ": " + imgErr[iid]
			}
		}
		for a, p := range imgWon {
			won[a] = p
		}
	}

	r.report(won)
	return out, errs
}

func (r *Resolver) resolveOnce(cookie, currentPlaceID string, ids []string) (map[string][]byte, map[string]string, map[string]string) {
	out := map[string][]byte{}
	errs := map[string]string{}
	won := map[string]string{}
	var mu sync.Mutex

	backend := r.knownPlaces(ids)

	byCreator := map[string][]string{}
	for _, chunk := range chunkStr(ids, 50) {
		info, err := r.assetsInfo(cookie, chunk)
		mu.Lock()
		for _, id := range chunk {
			c, ok := info[id]
			if !ok {
				if err != nil {
					errs[id] = "info: " + err.Error()
				} else {
					errs[id] = "asset info not found"
				}
				continue
			}
			byCreator[c] = append(byCreator[c], id)
		}
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for creator, cids := range byCreator {
		wg.Add(1)
		go func(creator string, cids []string) {
			defer wg.Done()

			seen := map[string]bool{}
			var cand []string
			add := func(p string) {
				if p != "" && !seen[p] {
					seen[p] = true
					cand = append(cand, p)
				}
			}
			add(currentPlaceID)
			for _, id := range cids {
				for _, p := range backend[id] {
					add(p)
				}
			}
			for _, p := range r.places(cookie, creator) {
				add(p)
			}
			if len(cand) == 0 {
				mu.Lock()
				for _, id := range cids {
					errs[id] = "no candidate places for " + creator
				}
				mu.Unlock()
				return
			}

			got, fail, gwon := r.fetchAcrossPlaces(cookie, cand, cids)
			mu.Lock()
			for id, b := range got {
				out[id] = b
				delete(errs, id)
			}
			for id, p := range gwon {
				won[id] = p
			}
			for id, e := range fail {
				if _, done := out[id]; !done {
					errs[id] = e
				}
			}
			mu.Unlock()
		}(creator, cids)
	}
	wg.Wait()
	return out, errs, won
}

func (r *Resolver) fetchAcrossPlaces(cookie string, places, ids []string) (map[string][]byte, map[string]string, map[string]string) {
	out := map[string][]byte{}
	won := map[string]string{}
	lastErr := map[string]string{}
	var mu sync.Mutex
	remaining := append([]string(nil), ids...)

	for _, place := range places {
		if len(remaining) == 0 {
			break
		}
		var still []string
		var wg sync.WaitGroup
		for _, chunk := range chunkStr(remaining, 50) {
			locs, err := r.batch(cookie, place, chunk)
			if err != nil {
				mu.Lock()
				for _, id := range chunk {
					lastErr[id] = "batch: " + err.Error()
					still = append(still, id)
				}
				mu.Unlock()
				continue
			}
			for i, id := range chunk {
				if i >= len(locs) || locs[i].url == "" {
					mu.Lock()
					if i < len(locs) && locs[i].err != "" {
						lastErr[id] = locs[i].err
					}
					still = append(still, id)
					mu.Unlock()
					continue
				}
				wg.Add(1)
				go func(id, url, place string) {
					defer wg.Done()
					data, derr := r.fetchCDN(url)
					if derr != nil {
						mu.Lock()
						lastErr[id] = "cdn: " + derr.Error()
						still = append(still, id)
						mu.Unlock()
						return
					}
					mu.Lock()
					out[id] = data
					won[id] = place
					mu.Unlock()
				}(id, locs[i].url, place)
			}
		}
		wg.Wait()
		remaining = still
	}

	fail := map[string]string{}
	for _, id := range remaining {
		if _, ok := out[id]; ok {
			continue
		}
		m := lastErr[id]
		if m == "" {
			m = "no access via any candidate place"
		}
		fail[id] = m
	}
	return out, fail, won
}

func (r *Resolver) knownPlaces(ids []string) map[string][]string {
	empty := map[string][]string{}
	if r.backendURL == "" || len(ids) == 0 {
		return empty
	}
	body, _ := json.Marshal(map[string]any{"assetIds": ids})
	req, _ := http.NewRequest("POST", r.backendURL+"/v1/known-places", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.acquire()
	resp, err := r.http.Do(req)
	r.release()
	if err != nil {
		return empty
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return empty
	}
	var out struct {
		Result map[string][]string `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Result == nil {
		return empty
	}
	return out.Result
}

func (r *Resolver) report(won map[string]string) {
	if r.backendURL == "" || len(won) == 0 {
		return
	}
	type pair struct {
		AssetID string `json:"assetId"`
		PlaceID string `json:"placeId"`
	}
	pairs := make([]pair, 0, len(won))
	for a, p := range won {
		pairs = append(pairs, pair{a, p})
	}
	body, _ := json.Marshal(map[string]any{"pairs": pairs})
	req, _ := http.NewRequest("POST", r.backendURL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if r.backendKey != "" {
		req.Header.Set("x-nexus-key", r.backendKey)
	}
	r.acquire()
	resp, err := r.http.Do(req)
	r.release()
	if err == nil {
		resp.Body.Close()
	}
}

func (r *Resolver) assetsInfo(cookie string, ids []string) (map[string]string, error) {
	req, _ := http.NewRequest("GET", developBase+strings.Join(ids, ","), nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cookie", ".ROBLOSECURITY="+cookie)

	r.acquire()
	resp, err := r.http.Do(req)
	r.release()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("develop status %d", resp.StatusCode)
	}

	var d struct {
		Data []struct {
			ID      int64 `json:"id"`
			Creator struct {
				Type     string `json:"type"`
				TargetID int64  `json:"targetId"`
			} `json:"creator"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(d.Data))
	for _, a := range d.Data {
		m[strconv.FormatInt(a.ID, 10)] = a.Creator.Type + ":" + strconv.FormatInt(a.Creator.TargetID, 10)
	}
	return m, nil
}

func (r *Resolver) places(cookie, creator string) []string {
	r.mu.Lock()
	if p, ok := r.placesCache[creator]; ok {
		r.mu.Unlock()
		return p
	}
	r.mu.Unlock()

	typ, id, ok := strings.Cut(creator, ":")
	if !ok {
		return nil
	}
	var url string
	if typ == "Group" {
		url = fmt.Sprintf("https://games.roblox.com/v2/groups/%s/games?accessFilter=2&limit=50", id)
	} else {
		url = fmt.Sprintf("https://games.roblox.com/v2/users/%s/games?limit=50", id)
	}

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cookie", ".ROBLOSECURITY="+cookie)

	r.acquire()
	resp, err := r.http.Do(req)
	r.release()
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var g struct {
		Data []struct {
			RootPlace struct {
				ID int64 `json:"id"`
			} `json:"rootPlace"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&g)
	places := make([]string, 0, len(g.Data))
	for _, it := range g.Data {
		if it.RootPlace.ID > 0 {
			places = append(places, strconv.FormatInt(it.RootPlace.ID, 10))
		}
	}

	r.mu.Lock()
	r.placesCache[creator] = places
	r.mu.Unlock()
	return places
}

func (r *Resolver) batch(cookie, placeID string, ids []string) ([]loc, error) {
	items := make([]map[string]any, len(ids))
	for i, id := range ids {
		n, _ := strconv.ParseInt(id, 10, 64)
		items[i] = map[string]any{"assetId": n, "requestId": id}
	}
	bodyJSON, _ := json.Marshal(items)

	req, _ := http.NewRequest("POST", batchURL, bytes.NewReader(bodyJSON))
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Roblox-Place-Id", placeID)
	req.Header.Set("Cookie", ".ROBLOSECURITY="+cookie)

	r.acquire()
	resp, err := r.http.Do(req)
	r.release()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var arr []struct {
		Locations []struct {
			Location string `json:"location"`
		} `json:"locations"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, err
	}
	out := make([]loc, len(arr))
	for i, a := range arr {
		if len(a.Locations) > 0 {
			out[i].url = a.Locations[0].Location
		}
		if len(a.Errors) > 0 {
			out[i].err = a.Errors[0].Message
		}
	}
	return out, nil
}

func (r *Resolver) fetchCDN(url string) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)

	r.acquire()
	resp, err := r.http.Do(req)
	r.release()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cdn status %d", resp.StatusCode)
	}
	if len(data) < 64 {
		return nil, fmt.Errorf("too small (%d bytes)", len(data))
	}
	return data, nil
}

func chunkStr(s []string, n int) [][]string {
	var out [][]string
	for i := 0; i < len(s); i += n {
		end := i + n
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}
