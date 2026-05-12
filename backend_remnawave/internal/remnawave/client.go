// Package remnawave is a thin HTTP client for the Remnawave panel REST API.
//
// Auth model: a single admin API token is configured at deployment. Every
// request must send all three of:
//   - Authorization: Bearer <token>
//   - X-Api-Key: <token>
//   - X-Forwarded-Proto: https
//
// Skipping X-Forwarded-Proto makes the panel reject the request.
//
// Known quirk: when posting users with externalSquadUuid set against a stale
// squad, the panel returns errorCode "A039". The client retries the same
// request once with that field cleared.
package remnawave

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			},
		},
	}
}

func (c *Client) Configured() bool { return c.token != "" && c.baseURL != "" }

// --- Types ---

const (
	StatusActive   = "ACTIVE"
	StatusDisabled = "DISABLED"
	StatusLimited  = "LIMITED"
	StatusExpired  = "EXPIRED"

	StrategyNoReset     = "NO_RESET"
	StrategyDay         = "DAY"
	StrategyWeek        = "WEEK"
	StrategyMonth       = "MONTH"
	StrategyMonthRolling = "MONTH_ROLLING"
)

type CreateUserRequest struct {
	Username             string    `json:"username"`
	Status               string    `json:"status,omitempty"`
	ExpireAt             time.Time `json:"expireAt"`
	TrafficLimitBytes    int64     `json:"trafficLimitBytes"`
	TrafficLimitStrategy string    `json:"trafficLimitStrategy,omitempty"`
	Email                string    `json:"email,omitempty"`
	Description          string    `json:"description,omitempty"`
	HwidDeviceLimit      int       `json:"hwidDeviceLimit,omitempty"`
	ActiveInternalSquads []string  `json:"activeInternalSquads,omitempty"`
	ExternalSquadUUID    string    `json:"externalSquadUuid,omitempty"`
}

type UpdateUserRequest struct {
	UUID                 uuid.UUID  `json:"uuid"`
	Status               string     `json:"status,omitempty"`
	ExpireAt             *time.Time `json:"expireAt,omitempty"`
	TrafficLimitBytes    *int64     `json:"trafficLimitBytes,omitempty"`
	TrafficLimitStrategy string     `json:"trafficLimitStrategy,omitempty"`
	Description          string     `json:"description,omitempty"`
	HwidDeviceLimit      *int       `json:"hwidDeviceLimit,omitempty"`
	ActiveInternalSquads []string   `json:"activeInternalSquads,omitempty"`
}

type User struct {
	UUID              uuid.UUID `json:"uuid"`
	ShortUUID         string    `json:"shortUuid"`
	Username          string    `json:"username"`
	Status            string    `json:"status"`
	SubscriptionURL   string    `json:"subscriptionUrl"`
	ExpireAt          time.Time `json:"expireAt"`
	TrafficLimitBytes int64     `json:"trafficLimitBytes"`
	UsedTrafficBytes  int64     `json:"usedTrafficBytes"`
	VlessUUID         string    `json:"vlessUuid,omitempty"`
	TrojanPassword    string    `json:"trojanPassword,omitempty"`
	SsPassword        string    `json:"ssPassword,omitempty"`
	HappLink          string    `json:"happLink,omitempty"`
}

type Squad struct {
	UUID uuid.UUID `json:"uuid"`
	Name string    `json:"name"`
}

type Node struct {
	UUID uuid.UUID `json:"uuid"`
	Name string    `json:"name"`
}

// --- HTTP plumbing ---

type apiError struct {
	Status    int
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
	RawBody   string
}

func (e *apiError) Error() string {
	if e.ErrorCode != "" {
		return fmt.Sprintf("remnawave: %s (code %s, http %d)", e.Message, e.ErrorCode, e.Status)
	}
	return fmt.Sprintf("remnawave: http %d: %s", e.Status, e.RawBody)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Api-Key", c.token)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "127.0.0.1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		e := &apiError{Status: resp.StatusCode, RawBody: string(raw)}
		_ = json.Unmarshal(raw, e)
		return e
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	// Remnawave wraps responses as {"response": {...}}. Unwrap if shape matches,
	// else fall back to decoding the body directly.
	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Response) > 0 {
		return json.Unmarshal(env.Response, out)
	}
	return json.Unmarshal(raw, out)
}

// --- Users ---

// CreateUser provisions a new Remnawave user. Implements the A039 retry:
// if the panel rejects the request with errorCode "A039" (stale external
// squad FK), the same payload is retried once with ExternalSquadUUID cleared.
func (c *Client) CreateUser(ctx context.Context, req CreateUserRequest) (*User, error) {
	if req.Status == "" {
		req.Status = StatusActive
	}
	if req.TrafficLimitStrategy == "" {
		req.TrafficLimitStrategy = StrategyNoReset
	}
	var u User
	err := c.do(ctx, "POST", "/api/users", req, &u)
	if e, ok := err.(*apiError); ok && e.ErrorCode == "A039" {
		req.ExternalSquadUUID = ""
		err = c.do(ctx, "POST", "/api/users", req, &u)
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) GetUser(ctx context.Context, userUUID uuid.UUID) (*User, error) {
	var u User
	if err := c.do(ctx, "GET", "/api/users/"+userUUID.String(), nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) UpdateUser(ctx context.Context, req UpdateUserRequest) (*User, error) {
	var u User
	if err := c.do(ctx, "PATCH", "/api/users", req, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) DeleteUser(ctx context.Context, userUUID uuid.UUID) error {
	return c.do(ctx, "DELETE", "/api/users/"+userUUID.String(), nil, nil)
}

func (c *Client) EnableUser(ctx context.Context, userUUID uuid.UUID) error {
	return c.do(ctx, "POST", "/api/users/"+userUUID.String()+"/actions/enable", nil, nil)
}

func (c *Client) DisableUser(ctx context.Context, userUUID uuid.UUID) error {
	return c.do(ctx, "POST", "/api/users/"+userUUID.String()+"/actions/disable", nil, nil)
}

// RevokeSubscription rotates the user's subscription URL. Returns the new user state.
func (c *Client) RevokeSubscription(ctx context.Context, userUUID uuid.UUID) (*User, error) {
	var u User
	if err := c.do(ctx, "POST", "/api/users/"+userUUID.String()+"/actions/revoke", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) ResetTraffic(ctx context.Context, userUUID uuid.UUID) error {
	return c.do(ctx, "POST", "/api/users/"+userUUID.String()+"/actions/reset-traffic", nil, nil)
}

// --- Squads / nodes ---

func (c *Client) ListInternalSquads(ctx context.Context) ([]Squad, error) {
	var out struct {
		InternalSquads []Squad `json:"internalSquads"`
	}
	if err := c.do(ctx, "GET", "/api/internal-squads", nil, &out); err != nil {
		// Fallback: some panels return a bare array.
		var arr []Squad
		if err2 := c.do(ctx, "GET", "/api/internal-squads", nil, &arr); err2 == nil {
			return arr, nil
		}
		return nil, err
	}
	return out.InternalSquads, nil
}

func (c *Client) ListNodes(ctx context.Context) ([]Node, error) {
	var out struct {
		Nodes []Node `json:"nodes"`
	}
	if err := c.do(ctx, "GET", "/api/nodes", nil, &out); err != nil {
		var arr []Node
		if err2 := c.do(ctx, "GET", "/api/nodes", nil, &arr); err2 == nil {
			return arr, nil
		}
		return nil, err
	}
	return out.Nodes, nil
}
