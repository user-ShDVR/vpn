package panel

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Client is a 3x-ui panel API client.
type Client struct {
	baseURL  string
	username string
	password string
	subURL   string // subscription service URL (e.g. http://ip:2096)
	subPath  string // subscription path (e.g. /mysecret/sub/)
	http     *http.Client
}

func New(panelURL, username, password, subURL, subPath string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  strings.TrimRight(panelURL, "/"),
		username: username,
		password: password,
		subURL:   strings.TrimRight(subURL, "/"),
		subPath:  subPath,
		http: &http.Client{
			Jar:     jar,
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

type XrayClient struct {
	ID         string `json:"id"`
	Flow       string `json:"flow"`
	Email      string `json:"email"`
	LimitIP    int    `json:"limitIp"`
	TotalGB    int64  `json:"totalGB"`    // bytes, 0 = unlimited
	ExpiryTime int64  `json:"expiryTime"` // unix ms, 0 = never, negative = relative days
	Enable     bool   `json:"enable"`
	TgID       string `json:"tgId"`
	SubID      string `json:"subId"`
	Comment    string `json:"comment"`
	Reset      int    `json:"reset"`
}

type Traffic struct {
	Up    int64 `json:"up"`
	Down  int64 `json:"down"`
	Total int64 `json:"total"`
}

type Inbound struct {
	ID             int    `json:"id"`
	Remark         string `json:"remark"`
	Protocol       string `json:"protocol"`
	Port           int    `json:"port"`
	Settings       string `json:"settings"`
	StreamSettings string `json:"streamSettings"`
	Enable         bool   `json:"enable"`
}

func (c *Client) Login(ctx context.Context) error {
	body := map[string]string{
		"username": c.username,
		"password": c.password,
	}
	data, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/login", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("panel login failed: %s", result.Msg)
	}
	log.Printf("[panel] login OK to %s", c.baseURL)
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any) ([]byte, error) {
	return c.doJSONRetry(ctx, method, path, body, false)
}

func (c *Client) doJSONRetry(ctx context.Context, method, path string, body any, retried bool) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	log.Printf("[panel] %s %s → %d, body=%s", method, path, resp.StatusCode, string(data))

	if resp.StatusCode == 401 && !retried {
		if loginErr := c.Login(ctx); loginErr != nil {
			return nil, loginErr
		}
		return c.doJSONRetry(ctx, method, path, body, true)
	}

	return data, nil
}

// doForm sends a POST with application/x-www-form-urlencoded (as 3x-ui panel expects).
func (c *Client) doForm(ctx context.Context, path string, formData url.Values) ([]byte, error) {
	return c.doFormRetry(ctx, path, formData, false)
}

func (c *Client) doFormRetry(ctx context.Context, path string, formData url.Values, retried bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	log.Printf("[panel] POST %s → %d, body=%s", path, resp.StatusCode, string(data))

	if resp.StatusCode == 401 && !retried {
		if loginErr := c.Login(ctx); loginErr != nil {
			return nil, loginErr
		}
		return c.doFormRetry(ctx, path, formData, true)
	}

	return data, nil
}

func (c *Client) AddClient(ctx context.Context, inboundID int, client XrayClient) error {
	// Build settings JSON exactly like 3x-ui web panel does
	settingsJSON, _ := json.Marshal(map[string]any{
		"clients": []XrayClient{client},
	})

	form := url.Values{}
	form.Set("id", fmt.Sprintf("%d", inboundID))
	form.Set("settings", string(settingsJSON))

	data, err := c.doForm(ctx, "/panel/api/inbounds/addClient", form)
	if err != nil {
		return err
	}

	var result struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode addClient response: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("addClient failed: %s", result.Msg)
	}

	return nil
}

// UpdateClient overwrites a single client's full record on the inbound. The
// uuid in the URL path identifies the existing client; the form-body holds the
// new settings (XrayClient with same uuid, updated fields).
func (c *Client) UpdateClient(ctx context.Context, inboundID int, clientUUID string, client XrayClient) error {
	settingsJSON, _ := json.Marshal(map[string]any{
		"clients": []XrayClient{client},
	})
	form := url.Values{}
	form.Set("id", fmt.Sprintf("%d", inboundID))
	form.Set("settings", string(settingsJSON))

	data, err := c.doForm(ctx, fmt.Sprintf("/panel/api/inbounds/updateClient/%s", clientUUID), form)
	if err != nil {
		return err
	}
	var result struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode updateClient response: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("updateClient failed: %s", result.Msg)
	}
	return nil
}

// RestartXray restarts the xray service on the panel so config changes take effect.
func (c *Client) RestartXray(ctx context.Context) {
	data, err := c.doJSON(ctx, "POST", "/panel/api/xray/restart", nil)
	if err != nil {
		log.Printf("[panel] xray restart failed: %v", err)
	} else {
		log.Printf("[panel] xray restart: %s", string(data))
	}
}

func (c *Client) DeleteClient(ctx context.Context, inboundID int, clientUUID string) error {
	data, err := c.doJSON(ctx, "POST",
		fmt.Sprintf("/panel/api/inbounds/%d/delClient/%s", inboundID, clientUUID),
		nil,
	)
	if err != nil {
		return err
	}

	var result struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode deleteClient response: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("deleteClient failed: %s", result.Msg)
	}
	return nil
}

// DeleteClientByEmail finds a client by email in the inbound and deletes it.
func (c *Client) DeleteClientByEmail(ctx context.Context, inboundID int, email string) error {
	inbound, err := c.GetInbound(ctx, inboundID)
	if err != nil {
		return fmt.Errorf("get inbound: %w", err)
	}

	// Parse settings JSON to find client UUID by email
	var settings struct {
		Clients []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"clients"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return fmt.Errorf("parse inbound settings: %w", err)
	}

	for _, cl := range settings.Clients {
		if cl.Email == email {
			return c.DeleteClient(ctx, inboundID, cl.ID)
		}
	}
	return fmt.Errorf("client with email %s not found", email)
}

func (c *Client) GetClientTraffic(ctx context.Context, email string) (*Traffic, error) {
	data, err := c.doJSON(ctx, "GET",
		fmt.Sprintf("/panel/api/inbounds/getClientTraffics/%s", email),
		nil,
	)
	if err != nil {
		return nil, err
	}

	var result struct {
		Success bool     `json:"success"`
		Obj     *Traffic `json:"obj"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode traffic response: %w", err)
	}
	return result.Obj, nil
}

// GetClientIPs returns the IPs currently associated with a client by xray email.
// 3x-ui v2 exposes this via POST /panel/api/inbounds/clientIps/:email — returns
// JSON where `obj` is either:
//   - sentinel string "No IP Record"
//   - newline-separated string "ip1\nip2" (older 3x-ui)
//   - array of "ip (timestamp)" strings (newer 3x-ui)
// Older builds use /panel/inbound/clientIps/:email — try both for compatibility.
func (c *Client) GetClientIPs(ctx context.Context, email string) ([]string, error) {
	paths := []string{
		fmt.Sprintf("/panel/api/inbounds/clientIps/%s", email),
		fmt.Sprintf("/panel/inbound/clientIps/%s", email),
	}
	var lastErr error
	for _, p := range paths {
		data, err := c.doJSON(ctx, "POST", p, nil)
		if err != nil {
			lastErr = err
			continue
		}
		var probe struct {
			Success bool            `json:"success"`
			Msg     string          `json:"msg"`
			Obj     json.RawMessage `json:"obj"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			lastErr = fmt.Errorf("decode clientIps response from %s: %w", p, err)
			continue
		}
		if !probe.Success {
			lastErr = fmt.Errorf("clientIps from %s not success: %s", p, probe.Msg)
			continue
		}
		// obj can be a JSON string OR a JSON array
		var asArr []string
		if err := json.Unmarshal(probe.Obj, &asArr); err == nil {
			return cleanIPs(asArr), nil
		}
		var asStr string
		if err := json.Unmarshal(probe.Obj, &asStr); err == nil {
			if asStr == "" || asStr == "No IP Record" {
				return nil, nil
			}
			return cleanIPs(strings.Split(asStr, "\n")), nil
		}
		lastErr = fmt.Errorf("clientIps from %s: unknown obj shape: %s", p, string(probe.Obj))
	}
	return nil, lastErr
}

// cleanIPs strips trailing " (timestamp)" suffixes and empties.
func cleanIPs(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if i := strings.Index(s, " ("); i > 0 {
			s = s[:i]
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// GetOnlineEmails returns the list of xray-emails currently connected across
// all inbounds on this panel. Works regardless of access-log config.
func (c *Client) GetOnlineEmails(ctx context.Context) ([]string, error) {
	data, err := c.doJSON(ctx, "POST", "/panel/api/inbounds/onlines", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Success bool     `json:"success"`
		Msg     string   `json:"msg"`
		Obj     []string `json:"obj"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode onlines response: %w", err)
	}
	if !result.Success {
		return nil, fmt.Errorf("onlines not success: %s", result.Msg)
	}
	return result.Obj, nil
}

func (c *Client) GetInbound(ctx context.Context, id int) (*Inbound, error) {
	data, err := c.doJSON(ctx, "GET",
		fmt.Sprintf("/panel/api/inbounds/get/%d", id),
		nil,
	)
	if err != nil {
		return nil, err
	}

	var result struct {
		Success bool     `json:"success"`
		Obj     *Inbound `json:"obj"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode inbound response: %w", err)
	}
	return result.Obj, nil
}

// GetSubURI fetches the client's VLESS URI from 3x-ui's subscription service.
// 3x-ui generates the full URI with all REALITY params (pbk, sid, sni, fp).
// Endpoint: GET /<subPath>/<subId> → base64-encoded VLESS links.
func (c *Client) GetSubURI(ctx context.Context, subID string) (string, error) {
	url := c.subURL + c.subPath + subID

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("subscription request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("subscription endpoint returned %d", resp.StatusCode)
	}

	// Response is base64-encoded, one VLESS link per line
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		// Some 3x-ui versions return plain text
		decoded = data
	}

	// Return the first link (there's one per inbound the client is on)
	lines := strings.Split(strings.TrimSpace(string(decoded)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "vless://") {
			return line, nil
		}
	}

	return "", fmt.Errorf("no VLESS link found in subscription response")
}

// GetSubURL returns the full subscription URL for the client.
// This URL can be imported directly into v2rayNG / Streisand / etc.
func (c *Client) GetSubURL(subID string) string {
	return c.subURL + c.subPath + subID
}
