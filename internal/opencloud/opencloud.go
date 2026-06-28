package opencloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

const (
	apiBase  = "https://apis.roblox.com/assets/v1"
	permBase = "https://apis.roblox.com/asset-permissions-api/v1"
)

type QuotaError struct {
	AssetType string
	ResetAt   string
}

func (e *QuotaError) Error() string {
	if e.ResetAt != "" {
		return fmt.Sprintf("monthly %s upload quota reached; resets %s", e.AssetType, e.ResetAt)
	}
	return fmt.Sprintf("monthly %s upload quota reached", e.AssetType)
}

type AuthError struct{ Code int }

func (e *AuthError) Error() string {
	return fmt.Sprintf("not authorized for this creator (HTTP %d)", e.Code)
}

type Uploader struct {
	http *http.Client
}

func New() *Uploader {
	return &Uploader{http: &http.Client{Timeout: 60 * time.Second}}
}

func mimeFor(assetType string) (string, string) {
	switch assetType {
	case "Audio":
		return "audio/ogg", "audio.ogg"
	case "Decal":
		return "image/png", "image.png"
	default:
		return "model/x-rbxm", "asset.rbxm"
	}
}

func (u *Uploader) buildBody(assetType string, data []byte, name string, isGroup bool, creatorId string) (*bytes.Buffer, string) {
	creator := map[string]string{}
	if isGroup {
		creator["groupId"] = creatorId
	} else {
		creator["userId"] = creatorId
	}
	reqJSON, _ := json.Marshal(map[string]any{
		"assetType":       assetType,
		"displayName":     truncate(name, 50),
		"description":     "Reuploaded via Nexus",
		"creationContext": map[string]any{"creator": creator},
	})
	contentType, filename := mimeFor(assetType)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("request", string(reqJSON))
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="fileContent"; filename="`+filename+`"`)
	h.Set("Content-Type", contentType)
	part, _ := w.CreatePart(h)
	_, _ = part.Write(data)
	_ = w.Close()
	return &body, w.FormDataContentType()
}

func (u *Uploader) Upload(apiKey, assetType string, data []byte, name string, isGroup bool, creatorId string) (string, error) {
	var opID string
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		body, ct := u.buildBody(assetType, data, name, isGroup, creatorId)
		req, _ := http.NewRequest("POST", apiBase+"/assets", body)
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("Content-Type", ct)

		resp, err := u.http.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(backoff(attempt))
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK:
			var op struct {
				OperationId string `json:"operationId"`
				Path        string `json:"path"`
			}
			_ = json.Unmarshal(respBody, &op)
			opID = op.OperationId
			if opID == "" {
				opID = strings.TrimPrefix(op.Path, "operations/")
			}
		case resp.StatusCode == http.StatusTooManyRequests:
			if q := parseQuota(assetType, respBody); q != nil {
				return "", q
			}
			lastErr = fmt.Errorf("rate limited")
			time.Sleep(backoff(attempt))
			continue
		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			return "", &AuthError{Code: resp.StatusCode}
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("server %d", resp.StatusCode)
			time.Sleep(backoff(attempt))
			continue
		default:
			return "", fmt.Errorf("upload status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
		}
		break
	}
	if opID == "" {
		if lastErr != nil {
			return "", fmt.Errorf("failed after retries: %w", lastErr)
		}
		return "", fmt.Errorf("no operation id returned")
	}
	return u.pollOperation(apiKey, opID)
}

func (u *Uploader) pollOperation(apiKey, opID string) (string, error) {
	delay := 200 * time.Millisecond
	for i := 0; i < 45; i++ {
		time.Sleep(delay)
		if delay < 1000*time.Millisecond {
			delay += 150 * time.Millisecond
		}
		req, _ := http.NewRequest("GET", apiBase+"/operations/"+opID, nil)
		req.Header.Set("x-api-key", apiKey)
		resp, err := u.http.Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var op struct {
			Done     bool `json:"done"`
			Response struct {
				AssetId string `json:"assetId"`
			} `json:"response"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &op)
		if op.Error != nil {
			return "", fmt.Errorf("%s", op.Error.Message)
		}
		if op.Done {
			if op.Response.AssetId == "" {
				return "", fmt.Errorf("done but no assetId: %s", truncate(string(b), 200))
			}
			return op.Response.AssetId, nil
		}
	}
	return "", fmt.Errorf("operation timed out")
}

// GrantUniverse authorizes a universe (experience) to use an asset. Verified
// format: PATCH /assets/permissions with x-api-key, a list of assetIds, and the
// subject at the top level. Needs the asset-permissions:write scope.
func (u *Uploader) GrantUniverse(apiKey, assetId, universeId string) error {
	payload := `{"assetIds":["` + assetId + `"],"subjectType":"Universe","subjectId":"` + universeId + `","action":"Use"}`
	req, _ := http.NewRequest("PATCH", permBase+"/assets/permissions", strings.NewReader(payload))
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := u.http.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return fmt.Errorf("grant status %d: %s", resp.StatusCode, truncate(string(body), 160))
}

func parseQuota(assetType string, body []byte) *QuotaError {
	var e struct{ Code, Message string }
	_ = json.Unmarshal(body, &e)
	if e.Code != "RESOURCE_EXHAUSTED" && !strings.Contains(e.Message, "has been reached") {
		return nil
	}
	reset := ""
	if i := strings.Index(e.Message, "try again at "); i >= 0 {
		reset = strings.Trim(strings.TrimSpace(e.Message[i+len("try again at "):]), ".")
	}
	return &QuotaError{AssetType: assetType, ResetAt: reset}
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
	if d > 16*time.Second {
		d = 16 * time.Second
	}
	return d
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
