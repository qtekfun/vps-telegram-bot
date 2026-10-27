// Package docker is a tiny Docker Engine API client. It issues only the calls
// listed in the README, so a path-allowlisting proxy can be configured for it.
package docker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxLogBytes bounds how much log output is read into memory.
const maxLogBytes = 4 << 20

var idRe = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

type Container struct {
	ID    string
	Name  string
	State string
}

type Detail struct {
	State        string
	Health       string // "", "starting", "healthy", "unhealthy"
	RestartCount int
	StartedAt    time.Time
	Tty          bool
}

type Client struct {
	base string
	http *http.Client
}

// New accepts DOCKER_HOST values of the form tcp://host:port or http://host:port.
func New(host string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(host)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("DOCKER_HOST %q: expected tcp://host:port", host)
	}
	switch u.Scheme {
	case "tcp", "http":
	default:
		return nil, fmt.Errorf("DOCKER_HOST scheme %q is not supported (use tcp://host:port to a socket proxy)", u.Scheme)
	}
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{base: "http://" + u.Host, http: hc}, nil
}

func checkID(id string) error {
	if !idRe.MatchString(id) {
		return errors.New("invalid container id")
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values) (*http.Response, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

func errFromResponse(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("docker: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}

// List returns all containers (GET /containers/json?all=1).
func (c *Client) List(ctx context.Context) ([]Container, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errFromResponse(resp)
	}
	var raw []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		State string   `json:"State"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("docker: decoding container list: %w", err)
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		name := ""
		for _, n := range r.Names {
			// Link aliases look like "/other/alias"; the real name has one slash.
			if n = strings.TrimPrefix(n, "/"); n != "" && !strings.Contains(n, "/") {
				name = n
				break
			}
		}
		if name != "" {
			out = append(out, Container{ID: r.ID, Name: name, State: r.State})
		}
	}
	return out, nil
}

// Inspect returns state details (GET /containers/{id}/json).
func (c *Client) Inspect(ctx context.Context, id string) (Detail, error) {
	if err := checkID(id); err != nil {
		return Detail{}, err
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil)
	if err != nil {
		return Detail{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Detail{}, errFromResponse(resp)
	}
	var raw struct {
		RestartCount int `json:"RestartCount"`
		State        struct {
			Status    string `json:"Status"`
			StartedAt string `json:"StartedAt"`
			Health    *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
		Config struct {
			Tty bool `json:"Tty"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&raw); err != nil {
		return Detail{}, fmt.Errorf("docker: decoding inspect: %w", err)
	}
	d := Detail{State: raw.State.Status, RestartCount: raw.RestartCount, Tty: raw.Config.Tty}
	if raw.State.Health != nil {
		d.Health = raw.State.Health.Status
	}
	if t, err := time.Parse(time.RFC3339Nano, raw.State.StartedAt); err == nil && t.Year() > 1 {
		d.StartedAt = t
	}
	return d, nil
}

// Logs returns the last tail lines (GET /containers/{id}/logs). tty must be the
// container's Config.Tty: TTY containers produce a raw stream, others a
// multiplexed one.
func (c *Client) Logs(ctx context.Context, id string, tail int, tty bool) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/logs", url.Values{
		"stdout": {"1"}, "stderr": {"1"}, "tail": {strconv.Itoa(tail)},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errFromResponse(resp)
	}
	body := io.LimitReader(resp.Body, maxLogBytes)
	if tty {
		b, err := io.ReadAll(body)
		return string(b), err
	}
	b, err := Demux(body)
	return string(b), err
}

// Action runs start, stop or restart (POST /containers/{id}/{action}).
// "already started/stopped" (304) counts as success.
func (c *Client) Action(ctx context.Context, id, action string) error {
	if err := checkID(id); err != nil {
		return err
	}
	switch action {
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("unsupported action %q", action)
	}
	var q url.Values
	if action != "start" {
		q = url.Values{"t": {"10"}}
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/"+action, q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		return nil
	}
	return errFromResponse(resp)
}

// Demux decodes Docker's multiplexed log stream (8-byte header per frame:
// stream type, 3 zero bytes, big-endian payload length) into plain text,
// stdout and stderr interleaved in arrival order. A stream cut off mid-frame
// yields the data read so far.
func Demux(r io.Reader) ([]byte, error) {
	var out []byte
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, nil
			}
			return out, err
		}
		if hdr[0] > 2 || hdr[1] != 0 || hdr[2] != 0 || hdr[3] != 0 {
			return out, errors.New("docker: unexpected log stream format")
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		frame, err := io.ReadAll(io.LimitReader(r, n))
		out = append(out, frame...)
		if err != nil {
			return out, err
		}
		if int64(len(frame)) < n {
			return out, nil
		}
	}
}
