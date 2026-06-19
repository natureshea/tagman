// Package clover is a read-only client for the Clover REST API. It only ever
// GETs: Thrive owns pricing and Clover is a downstream mirror we read from.
package clover

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config holds the credentials and target environment, supplied at startup.
//
//	BaseURL examples:
//	  production (North America): https://api.clover.com
//	  sandbox:                    https://apisandbox.dev.clover.com
type Config struct {
	BaseURL    string // no trailing slash
	MerchantID string
	APIToken   string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

// Item is the subset of Clover's item object we care about. Clover returns
// price as an integer number of cents (e.g. 1200 == $12.00).
type Item struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Price        int64  `json:"price"`        // cents
	PriceType    string `json:"priceType"`    // FIXED | VARIABLE | PER_UNIT
	Hidden       bool   `json:"hidden"`
	ModifiedTime int64  `json:"modifiedTime"` // unix milliseconds (13 digits)
}

// elementsResponse is Clover's standard list envelope: {"elements":[...]}.
type elementsResponse struct {
	Elements []Item `json:"elements"`
}

// cloverError surfaces non-2xx responses with the body for debugging.
type cloverError struct {
	Status int
	Body   string
}

func (e *cloverError) Error() string {
	return fmt.Sprintf("clover api: status=%d body=%s", e.Status, e.Body)
}

func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := c.cfg.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &cloverError{Status: resp.StatusCode, Body: string(buf)}
	}
	return buf, nil
}

const pageLimit = 100 // Clover default; hard cap is 1000.

// ListItems pulls the full catalog, paginating until a short/empty page.
// Hidden items are skipped.
func (c *Client) ListItems(ctx context.Context) ([]Item, error) {
	var out []Item
	offset := 0
	for {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(pageLimit))
		q.Set("offset", strconv.Itoa(offset))
		body, err := c.get(ctx, c.itemsPath(), q)
		if err != nil {
			return nil, err
		}
		var page elementsResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode items page: %w", err)
		}
		for _, it := range page.Elements {
			if !it.Hidden {
				out = append(out, it)
			}
		}
		if len(page.Elements) < pageLimit {
			break // last page
		}
		offset += pageLimit
	}
	return out, nil
}

// GetItem fetches one item's current state, used at render time for live price.
func (c *Client) GetItem(ctx context.Context, itemID string) (Item, error) {
	body, err := c.get(ctx, c.itemsPath()+"/"+url.PathEscape(itemID), nil)
	if err != nil {
		return Item{}, err
	}
	var it Item
	if err := json.Unmarshal(body, &it); err != nil {
		return Item{}, fmt.Errorf("decode item: %w", err)
	}
	return it, nil
}

// ChangedSince returns items modified at or after t (the polling primitive).
// Clover's modifiedTime is unix milliseconds, and the filter uses >=.
func (c *Client) ChangedSince(ctx context.Context, t time.Time) ([]Item, error) {
	ms := t.UnixMilli()
	var out []Item
	offset := 0
	for {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(pageLimit))
		q.Set("offset", strconv.Itoa(offset))
		q.Set("filter", "modifiedTime>="+strconv.FormatInt(ms, 10))
		body, err := c.get(ctx, c.itemsPath(), q)
		if err != nil {
			return nil, err
		}
		var page elementsResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode changed items: %w", err)
		}
		out = append(out, page.Elements...)
		if len(page.Elements) < pageLimit {
			break
		}
		offset += pageLimit
	}
	return out, nil
}

func (c *Client) itemsPath() string {
	return "/v3/merchants/" + url.PathEscape(c.cfg.MerchantID) + "/items"
}

// Dollars is a convenience for display: cents → dollars.
func (i Item) Dollars() float64 { return float64(i.Price) / 100.0 }
