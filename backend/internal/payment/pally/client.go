// Package pally is a client for the Pally / Pal24 merchant API.
//
// Docs: https://pally.info/en/reference/api (also https://pal24.pro)
//
// Auth: Authorization: Bearer {token}
// Base: https://pal24.pro/api/v1
//
// Postback signature: SignatureValue = strtoupper(md5(OutSum + ":" + InvId + ":" + apiToken))
package pally

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultBaseURL = "https://pal24.pro/api/v1"

type Client struct {
	token   string
	shopID  string
	baseURL string
	http    *http.Client
}

func New(token, shopID, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		token: token, shopID: shopID, baseURL: baseURL,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Configured() bool {
	return c.token != "" && c.shopID != ""
}

type CreateBillRequest struct {
	Amount              float64 // RUB, 2 decimals
	OrderID             string  // our payment.id
	Description         string
	Custom              string // arbitrary, returned in postback
	Name                string
	PayerPaysCommission bool
}

type CreateBillResponse struct {
	Success     string `json:"success"`
	LinkURL     string `json:"link_url"`
	LinkPageURL string `json:"link_page_url"`
	BillID      string `json:"bill_id"`
	Message     string `json:"message,omitempty"`
}

func (c *Client) CreateBill(ctx context.Context, req CreateBillRequest) (*CreateBillResponse, error) {
	form := url.Values{}
	form.Set("amount", fmt.Sprintf("%.2f", req.Amount))
	form.Set("order_id", req.OrderID)
	form.Set("description", req.Description)
	form.Set("type", "normal")
	form.Set("shop_id", c.shopID)
	form.Set("currency_in", "RUB")
	if req.Custom != "" {
		form.Set("custom", req.Custom)
	}
	if req.Name != "" {
		form.Set("name", req.Name)
	}
	if req.PayerPaysCommission {
		form.Set("payer_pays_commission", "1")
	} else {
		form.Set("payer_pays_commission", "0")
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/bill/create", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("pally request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("pally http %d: %s", resp.StatusCode, string(body))
	}

	var out CreateBillResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("pally decode: %w (body=%s)", err, string(body))
	}
	if out.Success != "true" && out.Success != "1" {
		return nil, fmt.Errorf("pally not success: %s", string(body))
	}
	return &out, nil
}

// VerifyPostbackSignature checks the SignatureValue from a Pally postback.
//
// Formula (per docs): strtoupper(md5(OutSum + ":" + InvId + ":" + apiToken)).
// OutSum is the amount string exactly as Pally sent it (e.g. "18.54").
func (c *Client) VerifyPostbackSignature(outSum, invID, signature string) bool {
	want := signPostback(outSum, invID, c.token)
	return strings.EqualFold(want, signature)
}

func signPostback(outSum, invID, token string) string {
	h := md5.Sum([]byte(outSum + ":" + invID + ":" + token))
	return strings.ToUpper(hex.EncodeToString(h[:]))
}
