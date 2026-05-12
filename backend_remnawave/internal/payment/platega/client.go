// Package platega is an HTTP client for the Platega.io merchant API.
//
// Docs: https://app.platega.io (see bedolaga reference impl).
//
// Auth: every request carries two static headers - X-MerchantId, X-Secret.
// There is NO webhook signature; reconciliation is "redirect + poll": after
// the user returns from the hosted payment page (or in a background poller),
// call GetTransaction with the merchant-side transactionId to check status.
//
// We pass our own `payments.id` as the `payload` field so the poller can
// correlate Platega transactions back to our DB rows.
package platega

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://app.platega.io"

type Client struct {
	merchantID    string
	secret        string
	baseURL       string
	paymentMethod int
	http          *http.Client
}

func New(merchantID, secret, baseURL string, paymentMethod int) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if paymentMethod == 0 {
		paymentMethod = 1
	}
	return &Client{
		merchantID:    merchantID,
		secret:        secret,
		baseURL:       strings.TrimRight(baseURL, "/"),
		paymentMethod: paymentMethod,
		http:          &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) Configured() bool { return c.merchantID != "" && c.secret != "" }

type PaymentDetails struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type CreatePaymentRequest struct {
	PaymentMethod  int            `json:"paymentMethod"`
	PaymentDetails PaymentDetails `json:"paymentDetails"`
	Description    string         `json:"description"`
	Return         string         `json:"return"`
	FailedURL      string         `json:"failedUrl"`
	Payload        string         `json:"payload"`
}

type CreatePaymentResponse struct {
	ID            string `json:"id"`
	RedirectURL   string `json:"redirect"`
	RedirectAlt   string `json:"redirectUrl"`
	URL           string `json:"url"`
	ExpiresIn     string `json:"expiresIn"`
	Status        string `json:"status"`
	Raw           json.RawMessage
}

// PayURL returns the first non-empty redirect-style URL the response provided.
func (r *CreatePaymentResponse) PayURL() string {
	for _, v := range []string{r.RedirectURL, r.RedirectAlt, r.URL} {
		if v != "" {
			return v
		}
	}
	return ""
}

type Transaction struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"`
	Amount  float64 `json:"amount"`
	Payload string  `json:"payload"`
	Raw     json.RawMessage
}

// CreatePayment creates a hosted payment on Platega and returns the redirect
// URL the user should be sent to. `description` is truncated to 64 bytes (UTF-8
// safe).
func (c *Client) CreatePayment(ctx context.Context, amountRubles float64, description, returnURL, failedURL, payload string) (*CreatePaymentResponse, error) {
	body := CreatePaymentRequest{
		PaymentMethod:  c.paymentMethod,
		PaymentDetails: PaymentDetails{Amount: amountRubles, Currency: "RUB"},
		Description:    truncateBytes(description, 64),
		Return:         returnURL,
		FailedURL:      failedURL,
		Payload:        payload,
	}
	var resp CreatePaymentResponse
	raw, err := c.doRetry(ctx, "POST", "/transaction/process", body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("platega decode create: %w (body=%s)", err, string(raw))
	}
	resp.Raw = raw
	return &resp, nil
}

// GetTransaction fetches Platega's view of a transaction by its merchant-side ID.
func (c *Client) GetTransaction(ctx context.Context, transactionID string) (*Transaction, error) {
	raw, err := c.doRetry(ctx, "GET", "/transaction/"+transactionID, nil)
	if err != nil {
		return nil, err
	}
	var t Transaction
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("platega decode get: %w (body=%s)", err, string(raw))
	}
	t.Raw = raw
	return &t, nil
}

// IsConfirmed returns true if the transaction status indicates a successful payment.
// Platega uses uppercase status strings; the exact set varies (CONFIRMED, SUCCESS,
// COMPLETED), so accept any of them.
func IsConfirmed(status string) bool {
	switch strings.ToUpper(status) {
	case "CONFIRMED", "SUCCESS", "COMPLETED", "PAID":
		return true
	}
	return false
}

// IsFailed returns true if the transaction definitively failed (no further polling needed).
func IsFailed(status string) bool {
	switch strings.ToUpper(status) {
	case "FAILED", "CANCELED", "CANCELLED", "REJECTED", "EXPIRED":
		return true
	}
	return false
}

// doRetry: 3x linear backoff on 5xx; other errors fail fast.
func (c *Client) doRetry(ctx context.Context, method, path string, body any) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		raw, status, err := c.do(ctx, method, path, body)
		if err == nil && status < 500 {
			if status >= 400 {
				return nil, fmt.Errorf("platega http %d: %s", status, string(raw))
			}
			return raw, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("platega http %d: %s", status, string(raw))
		}
	}
	return nil, lastErr
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-MerchantId", c.merchantID)
	req.Header.Set("X-Secret", c.secret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, nil
}

// truncateBytes returns s shortened to at most n bytes, never splitting a
// multibyte UTF-8 codepoint. Mirrors the safety the bedolaga client documents.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}
