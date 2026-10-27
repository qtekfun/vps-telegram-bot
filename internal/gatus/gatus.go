// Package gatus reads endpoint statuses from the Gatus API.
package gatus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
)

const pageSize = 100

// Endpoint is the latest known state of one Gatus endpoint.
type Endpoint struct {
	Group    string
	Name     string
	Success  bool
	HasData  bool // false when Gatus has no result yet
	Failures []string
}

type Client struct {
	base string
	http *http.Client
}

func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{base: baseURL, http: hc}
}

type status struct {
	Name    string `json:"name"`
	Group   string `json:"group"`
	Results []struct {
		Success          bool     `json:"success"`
		Errors           []string `json:"errors"`
		ConditionResults []struct {
			Condition string `json:"condition"`
			Success   bool   `json:"success"`
		} `json:"conditionResults"`
	} `json:"results"`
}

// Statuses fetches every page of /api/v1/endpoints/statuses.
func (c *Client) Statuses(ctx context.Context) ([]Endpoint, error) {
	var all []Endpoint
	for page := 1; page <= 10; page++ {
		url := fmt.Sprintf("%s/api/v1/endpoints/statuses?page=%d&pageSize=%d", c.base, page, pageSize)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		eps, n, err := decode(resp)
		if err != nil {
			return nil, err
		}
		all = append(all, eps...)
		if n < pageSize {
			break
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Group != all[j].Group {
			return all[i].Group < all[j].Group
		}
		return all[i].Name < all[j].Name
	})
	return all, nil
}

func decode(resp *http.Response) ([]Endpoint, int, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("gatus: HTTP %s", strconv.Itoa(resp.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, err
	}
	return Parse(b)
}

// Parse converts a statuses payload; it also returns the raw item count.
func Parse(b []byte) ([]Endpoint, int, error) {
	var raw []status
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, 0, fmt.Errorf("gatus: decoding statuses: %w", err)
	}
	out := make([]Endpoint, 0, len(raw))
	for _, s := range raw {
		e := Endpoint{Group: s.Group, Name: s.Name}
		if n := len(s.Results); n > 0 {
			last := s.Results[n-1]
			e.HasData, e.Success = true, last.Success
			for _, cr := range last.ConditionResults {
				if !cr.Success {
					e.Failures = append(e.Failures, cr.Condition)
				}
			}
			e.Failures = append(e.Failures, last.Errors...)
		}
		out = append(out, e)
	}
	return out, len(raw), nil
}
