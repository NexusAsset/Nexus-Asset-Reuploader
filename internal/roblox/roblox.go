package roblox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var client = &http.Client{Timeout: 12 * time.Second}

type Info struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Kind        string `json:"kind"`
}

func ResolveUser(id string) (Info, error) {
	resp, err := client.Get("https://users.roblox.com/v1/users/" + id)
	if err != nil {
		return Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Info{}, fmt.Errorf("user lookup %d", resp.StatusCode)
	}
	var u struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return Info{}, err
	}
	return Info{Name: u.Name, DisplayName: u.DisplayName, Kind: "user"}, nil
}

func ResolveGroup(id string) (Info, error) {
	resp, err := client.Get("https://groups.roblox.com/v1/groups/" + id)
	if err != nil {
		return Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Info{}, fmt.Errorf("group lookup %d", resp.StatusCode)
	}
	var g struct {
		Name  string `json:"name"`
		Owner struct {
			Username string `json:"username"`
		} `json:"owner"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return Info{}, err
	}
	return Info{Name: g.Name, DisplayName: g.Name, Kind: "group"}, nil
}

func Resolve(id string, isGroup bool) (Info, error) {
	if isGroup {
		return ResolveGroup(id)
	}
	return ResolveUser(id)
}

type Profile struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	AvatarUrl   string `json:"avatarUrl"`
}

func Authenticated(cookie string) (Profile, error) {
	req, _ := http.NewRequest("GET", "https://users.roblox.com/v1/users/authenticated", nil)
	req.Header.Set("Cookie", ".ROBLOSECURITY="+cookie)
	resp, err := client.Do(req)
	if err != nil {
		return Profile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Profile{}, fmt.Errorf("auth lookup %d", resp.StatusCode)
	}
	var u struct {
		Id          json.Number `json:"id"`
		Name        string      `json:"name"`
		DisplayName string      `json:"displayName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return Profile{}, err
	}
	id := u.Id.String()
	return Profile{Id: id, Name: u.Name, DisplayName: u.DisplayName, AvatarUrl: HeadshotURL(id)}, nil
}

func thumb(url string) string {
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var t struct {
		Data []struct {
			ImageUrl string `json:"imageUrl"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&t) == nil && len(t.Data) > 0 {
		return t.Data[0].ImageUrl
	}
	return ""
}

func HeadshotURL(userId string) string {
	return thumb("https://thumbnails.roblox.com/v1/users/avatar-headshot?userIds=" + userId + "&size=150x150&format=Png&isCircular=false")
}

func GroupIconURL(groupId string) string {
	return thumb("https://thumbnails.roblox.com/v1/groups/icons?groupIds=" + groupId + "&size=150x150&format=Png&isCircular=false")
}

func AvatarURL(id string, isGroup bool) string {
	if isGroup {
		return GroupIconURL(id)
	}
	return HeadshotURL(id)
}
