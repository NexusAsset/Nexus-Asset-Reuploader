package download

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Downloader struct {
	http *http.Client
}

func New() *Downloader {
	return &Downloader{http: &http.Client{Timeout: 30 * time.Second}}
}

func (d *Downloader) Asset(cookie, id string) ([]byte, error) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return nil, fmt.Errorf("no cookie set (audio/images need one)")
	}
	req, _ := http.NewRequest("GET", "https://assetdelivery.roblox.com/v1/asset/?id="+id, nil)
	req.Header.Set("User-Agent", "RobloxStudio/WinInet")
	req.Header.Set("Cookie", ".ROBLOSECURITY="+cookie)
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download status %d (no access?)", resp.StatusCode)
	}
	if len(data) < 64 {
		return nil, fmt.Errorf("download too small (%d bytes) - likely no access", len(data))
	}
	return data, nil
}
