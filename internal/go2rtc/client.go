// Package go2rtc is a minimal read-only client for the go2rtc HTTP API.
package go2rtc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxFrameBytes = 10 << 20

type Client struct {
	base     string
	user     string
	password string
	http     *http.Client
}

func New(baseURL, user, password string) *Client {
	return &Client{
		base:     strings.TrimRight(baseURL, "/"),
		user:     user,
		password: password,
		http:     &http.Client{Timeout: 20 * time.Second},
	}
}

// Streams returns the names of the configured streams, sorted.
// Only names are returned; stream sources (which often contain camera
// credentials) are never read past the top-level keys.
func (c *Client) Streams(ctx context.Context) ([]string, error) {
	body, _, err := c.get(ctx, "/api/streams", 4<<20)
	if err != nil {
		return nil, err
	}
	var streams map[string]json.RawMessage
	if err := json.Unmarshal(body, &streams); err != nil {
		return nil, fmt.Errorf("go2rtc /api/streams: unexpected response: %w", err)
	}
	names := make([]string, 0, len(streams))
	for name := range streams {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// StreamStatus is the safe subset of a stream's state in /api/streams.
type StreamStatus struct {
	Name      string
	Active    bool     // go2rtc is connected to the camera right now
	Media     []string // e.g. "video, recvonly, H264"; only known while active
	Consumers int
}

// Producer and stream shapes declare only fields that are safe to expose.
// Everything else (url, remote_addr, sdp, user_agent, ...) is dropped while decoding.
type rawProducer struct {
	FormatName string            `json:"format_name"`
	Medias     []json.RawMessage `json:"medias"`
}

type rawStream struct {
	Producers []rawProducer     `json:"producers"`
	Consumers []json.RawMessage `json:"consumers"`
}

// Status returns the state of every stream, sorted by name.
// go2rtc connects to cameras on demand, so an inactive stream is idle, not necessarily offline.
func (c *Client) Status(ctx context.Context) ([]StreamStatus, error) {
	body, _, err := c.get(ctx, "/api/streams", 4<<20)
	if err != nil {
		return nil, err
	}
	var streams map[string]rawStream
	if err := json.Unmarshal(body, &streams); err != nil {
		return nil, fmt.Errorf("go2rtc /api/streams: unexpected response: %w", err)
	}
	out := make([]StreamStatus, 0, len(streams))
	for name, st := range streams {
		s := StreamStatus{Name: name, Consumers: len(st.Consumers)}
		seen := map[string]bool{}
		for _, p := range st.Producers {
			// An idle producer is only {"url": ...}; a connected one carries connection details.
			if p.FormatName == "" && len(p.Medias) == 0 {
				continue
			}
			s.Active = true
			for _, raw := range p.Medias {
				var m string
				if json.Unmarshal(raw, &m) == nil && !seen[m] {
					seen[m] = true
					s.Media = append(s.Media, m)
				}
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Frame returns a JPEG snapshot of the named stream. width <= 0 keeps the native size.
func (c *Client) Frame(ctx context.Context, stream string, width int) ([]byte, error) {
	q := url.Values{"src": {stream}}
	if width > 0 {
		q.Set("width", strconv.Itoa(width))
	}
	body, ctype, err := c.get(ctx, "/api/frame.jpeg?"+q.Encode(), maxFrameBytes)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(ctype, "image/") || !bytes.HasPrefix(body, []byte{0xFF, 0xD8}) {
		return nil, fmt.Errorf("go2rtc returned %q instead of a JPEG (is the camera online?)", ctype)
	}
	return body, nil
}

// Ping checks that go2rtc is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, _, err := c.get(ctx, "/api", 1<<20)
	return err
}

func (c *Client) get(ctx context.Context, path string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, "", err
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, "", fmt.Errorf("go2rtc %s: %s: %s", strings.SplitN(path, "?", 2)[0], resp.Status, msg)
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("go2rtc %s: response larger than %d bytes", strings.SplitN(path, "?", 2)[0], limit)
	}
	return body, resp.Header.Get("Content-Type"), nil
}
