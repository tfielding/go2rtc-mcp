package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tfielding/go2rtc-mcp/internal/go2rtc"
	"github.com/tfielding/go2rtc-mcp/internal/tools"
)

var fakeJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 'f', 'a', 'k', 'e', 0xFF, 0xD9}

// fakeGo2rtc mimics the parts of the go2rtc API we use, including stream
// sources that contain credentials (which must never leak).
type fakeGo2rtc struct {
	mu       sync.Mutex
	frameReq []string // "src|width"
	offline  map[string]bool
}

func (f *fakeGo2rtc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "secret" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api":
		w.Write([]byte(`{"version":"1.9.9"}`))
	case "/api/streams":
		w.Write([]byte(`{
		  "driveway": {"producers":[{"url":"rtsp://user:CAMPASS@10.0.0.20/stream1"}],"consumers":[]},
		  "Back_Yard": {"producers":[{"url":"rtsp://user:CAMPASS@10.0.0.21/stream1"}]},
		  "garage": {"producers":[{"url":"rtsp://user:CAMPASS@10.0.0.22/stream1"}]}
		}`))
	case "/api/frame.jpeg":
		src := r.URL.Query().Get("src")
		f.mu.Lock()
		f.frameReq = append(f.frameReq, src+"|"+r.URL.Query().Get("width"))
		off := f.offline[src]
		f.mu.Unlock()
		if off {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("streams: wrong user/pass"))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(fakeJPEG)
	default:
		http.NotFound(w, r)
	}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func start(t *testing.T, cfg config) (*mcp.ClientSession, *fakeGo2rtc, *httptest.Server) {
	t.Helper()
	fake := &fakeGo2rtc{offline: map[string]bool{"garage": true}}
	g := httptest.NewServer(fake)
	t.Cleanup(g.Close)
	if cfg.opt.Allowed == nil {
		cfg.opt.Allowed = map[string]bool{}
	}
	srv := httptest.NewServer(newHandler(go2rtc.New(g.URL, "admin", "secret"), cfg))
	t.Cleanup(srv.Close)
	return connect(t, srv.URL, cfg.token), fake, srv
}

func connect(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             url + "/mcp",
		HTTPClient:           &http.Client{Transport: bearer{token}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestToolsAreListedReadOnly(t *testing.T) {
	cs, _, _ := start(t, config{opt: tools.Options{DefaultWidth: 1280, MaxWidth: 1920}})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s not marked read-only", tool.Name)
		}
	}
	if len(names) != 2 || !names["list_cameras"] || !names["get_snapshot"] {
		t.Fatalf("tools: %v", names)
	}
}

func TestListCamerasNoCredentialLeak(t *testing.T) {
	cs, _, _ := start(t, config{opt: tools.Options{DefaultWidth: 1280}})
	res := call(t, cs, "list_cameras", nil)
	raw, _ := json.Marshal(res)
	for _, secret := range []string{"CAMPASS", "rtsp://", "10.0.0."} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("leaked %q: %s", secret, raw)
		}
	}
	var out tools.ListOutput
	b, _ := json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &out)
	if strings.Join(out.Cameras, ",") != "Back_Yard,driveway,garage" {
		t.Fatalf("cameras: %v (%s)", out.Cameras, raw)
	}
}

func TestAllowlist(t *testing.T) {
	cs, fake, _ := start(t, config{opt: tools.Options{Allowed: map[string]bool{"driveway": true}}})
	res := call(t, cs, "list_cameras", nil)
	if b, _ := json.Marshal(res.StructuredContent); string(b) != `{"cameras":["driveway"]}` {
		t.Fatalf("got %s", b)
	}
	res = call(t, cs, "get_snapshot", map[string]any{"camera": "Back_Yard"})
	if !res.IsError || !strings.Contains(text(res), `unknown camera "Back_Yard"`) {
		t.Fatalf("expected refusal, got %+v", res)
	}
	if len(fake.frameReq) != 0 {
		t.Fatalf("disallowed camera reached go2rtc: %v", fake.frameReq)
	}
}

func TestSnapshot(t *testing.T) {
	cs, fake, _ := start(t, config{opt: tools.Options{DefaultWidth: 1280, MaxWidth: 1920}})

	res := call(t, cs, "get_snapshot", map[string]any{"camera": "DRIVEWAY"}) // case-insensitive
	if res.IsError {
		t.Fatalf("error: %s", text(res))
	}
	var img *mcp.ImageContent
	for _, c := range res.Content {
		if ic, ok := c.(*mcp.ImageContent); ok {
			img = ic
		}
	}
	if img == nil || img.MIMEType != "image/jpeg" || !bytes.Equal(img.Data, fakeJPEG) {
		t.Fatalf("image: %+v", res.Content)
	}
	if !strings.Contains(text(res), "Snapshot from driveway at") {
		t.Fatalf("text: %q", text(res))
	}

	call(t, cs, "get_snapshot", map[string]any{"camera": "driveway", "width": 640})
	call(t, cs, "get_snapshot", map[string]any{"camera": "driveway", "width": 99999})
	want := []string{"driveway|1280", "driveway|640", "driveway|1920"}
	if strings.Join(fake.frameReq, " ") != strings.Join(want, " ") {
		t.Fatalf("frame requests: %v, want %v", fake.frameReq, want)
	}
}

func TestSnapshotErrors(t *testing.T) {
	cs, fake, _ := start(t, config{})
	res := call(t, cs, "get_snapshot", map[string]any{"camera": "rtsp://evil/stream"})
	if !res.IsError || !strings.Contains(text(res), "available: Back_Yard, driveway, garage") {
		t.Fatalf("got %+v", text(res))
	}
	res = call(t, cs, "get_snapshot", map[string]any{"camera": "garage"}) // go2rtc returns text, not a JPEG
	if !res.IsError || !strings.Contains(text(res), "instead of a JPEG") {
		t.Fatalf("got %q", text(res))
	}
	if strings.Join(fake.frameReq, " ") != "garage|" {
		t.Fatalf("frame requests: %v", fake.frameReq)
	}
}

func TestAuthToken(t *testing.T) {
	cs, _, srv := start(t, config{token: "s3cret"})
	if res := call(t, cs, "list_cameras", nil); res.IsError {
		t.Fatalf("with token: %s", text(res))
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	_, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{"wrong"}}, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err == nil {
		t.Fatal("expected wrong token to be rejected")
	}
	resp, _ := http.Get(srv.URL + "/healthz") // health check needs no token
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
}

func TestHealthzReportsGo2rtcDown(t *testing.T) {
	srv := httptest.NewServer(newHandler(go2rtc.New("http://127.0.0.1:1", "", ""), config{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("GO2RTC_URL", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error without GO2RTC_URL")
	}
	t.Setenv("GO2RTC_URL", "http://go2rtc:1984")
	t.Setenv("CAMERAS", " Driveway , garage,")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.opt.Allowed) != 2 || !c.opt.Allowed["driveway"] || c.opt.DefaultWidth != 1280 || c.listen != ":8090" {
		t.Fatalf("%+v", c)
	}
}
