// Package tools defines the MCP tools. Everything here is read-only.
package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tfielding/go2rtc-mcp/internal/go2rtc"
)

// Source is the camera backend (implemented by go2rtc.Client).
type Source interface {
	Streams(ctx context.Context) ([]string, error)
	Frame(ctx context.Context, stream string, width int) ([]byte, error)
	Status(ctx context.Context) ([]go2rtc.StreamStatus, error)
}

type Options struct {
	Allowed      map[string]bool // lowercased camera names; empty = all cameras
	Exclude      []string        // lowercased glob patterns (e.g. "*_sub") of cameras to hide
	DefaultWidth int             // snapshot width in pixels; 0 = native
	MaxWidth     int             // cap on requested width; 0 = no cap
	CacheTTL     time.Duration   // reuse a camera's frame for this long; 0 = always fetch
}

type ListOutput struct {
	Cameras []string `json:"cameras" jsonschema:"camera names usable with get_snapshot"`
}

type SnapshotInput struct {
	Camera string `json:"camera" jsonschema:"camera name, as returned by list_cameras"`
	Width  int    `json:"width,omitempty" jsonschema:"optional image width in pixels; smaller is faster"`
}

type SnapshotsInput struct {
	Cameras []string `json:"cameras,omitempty" jsonschema:"camera names, as returned by list_cameras; omit for all cameras"`
	Width   int      `json:"width,omitempty" jsonschema:"optional image width in pixels for every image; smaller is faster"`
}

type StatusInput struct {
	Cameras []string `json:"cameras,omitempty" jsonschema:"camera names, as returned by list_cameras; omit for all cameras"`
	Check   bool     `json:"check,omitempty" jsonschema:"take a small test frame from each idle camera to find out whether it is online or offline; slower"`
}

type StatusOutput struct {
	Cameras []CameraStatus `json:"cameras"`
}

// Camera states reported by camera_status.
const (
	StateStreaming = "streaming" // go2rtc is receiving video now, so the camera is online
	StateIdle      = "idle"      // nobody is watching, so go2rtc isn't connected; says nothing about the camera
	StateOnline    = "online"    // idle, but a test frame worked (check)
	StateOffline   = "offline"   // idle, and a test frame failed (check)
)

type CameraStatus struct {
	Camera  string `json:"camera"`
	State   string `json:"state" jsonschema:"streaming: receiving video now, so online. idle: nobody is watching so go2rtc is not connected; this is normal and does not mean offline. online/offline: result of check for an idle camera"`
	Video   string `json:"video,omitempty" jsonschema:"video codec while streaming, e.g. H264 or H265"`
	Audio   string `json:"audio,omitempty" jsonschema:"audio codec while streaming, e.g. PCMA/8000; absent if the camera sends no audio"`
	Viewers int    `json:"viewers" jsonschema:"clients currently watching the stream"`
}

// checkWidth is the frame width used by camera_status check; small, since the image is discarded.
const checkWidth = 320

// maxParallel limits concurrent frame requests to go2rtc in get_snapshots.
const maxParallel = 4

// Register adds the camera tools to s.
func Register(s *mcp.Server, src Source, opt Options) {
	t := &toolset{src: src, opt: opt, cache: newFrameCache(opt.CacheTTL)}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_cameras",
		Description: "List the cameras that snapshots can be taken from.",
		Annotations: readOnly("List cameras"),
	}, t.list)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_snapshot",
		Description: "Take a live snapshot (JPEG image) from a camera. Use list_cameras to find names.",
		Annotations: readOnly("Camera snapshot"),
	}, t.snapshot)
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_snapshots",
		Description: "Take live snapshots (JPEG images) from several cameras at once, or all cameras if none are named. " +
			"Each image is preceded by a line naming its camera. Cameras that fail are reported without failing the rest.",
		Annotations: readOnly("Camera snapshots"),
	}, t.snapshots)
	mcp.AddTool(s, &mcp.Tool{
		Name: "camera_status",
		Description: "Show each camera's state (streaming, idle, or with check online/offline), its codecs and how many clients are watching. " +
			"go2rtc only connects to a camera while someone watches it, so idle is normal and not a fault; use check to find out whether idle cameras are online.",
		Annotations: readOnly("Camera status"),
	}, t.status)
}

func readOnly(title string) *mcp.ToolAnnotations {
	f := false
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, DestructiveHint: &f, OpenWorldHint: &f}
}

type toolset struct {
	src   Source
	opt   Options
	cache *frameCache
}

func (t *toolset) cameras(ctx context.Context) ([]string, error) {
	all, err := t.src.Streams(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range all {
		if t.visible(name) {
			out = append(out, name)
		}
	}
	return out, nil
}

// visible applies the allowlist and the exclude patterns, both case-insensitive.
func (t *toolset) visible(name string) bool {
	lower := strings.ToLower(name)
	if len(t.opt.Allowed) > 0 && !t.opt.Allowed[lower] {
		return false
	}
	for _, pat := range t.opt.Exclude {
		if ok, _ := path.Match(pat, lower); ok {
			return false
		}
	}
	return true
}

func (t *toolset) list(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListOutput, error) {
	cams, err := t.cameras(ctx)
	if err != nil {
		slog.Error("list_cameras", "err", err)
		return nil, ListOutput{}, err
	}
	slog.Info("list_cameras", "count", len(cams))
	if cams == nil {
		cams = []string{}
	}
	return nil, ListOutput{Cameras: cams}, nil
}

func (t *toolset) snapshot(ctx context.Context, _ *mcp.CallToolRequest, in SnapshotInput) (*mcp.CallToolResult, any, error) {
	cams, err := t.cameras(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Only known, allowed stream names are passed to go2rtc, never arbitrary sources.
	name, ok := find(cams, in.Camera)
	if !ok {
		return nil, nil, unknownCamera(in.Camera, cams)
	}
	width := t.width(in.Width)
	f, err := t.frame(ctx, name, width)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: fmt.Sprintf("Snapshot from %s at %s", name, stamp(f.at))},
		&mcp.ImageContent{Data: f.jpeg, MIMEType: "image/jpeg"},
	}}, nil, nil
}

func (t *toolset) snapshots(ctx context.Context, _ *mcp.CallToolRequest, in SnapshotsInput) (*mcp.CallToolResult, any, error) {
	cams, err := t.cameras(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Resolve every name before contacting go2rtc, so one bad name sends no requests at all.
	names, err := resolve(cams, in.Cameras)
	if err != nil {
		return nil, nil, err
	}
	shots := t.frames(ctx, names, t.width(in.Width))

	content := []mcp.Content{}
	failed := 0
	for i, name := range names {
		if err := shots[i].err; err != nil {
			failed++
			content = append(content, &mcp.TextContent{Text: fmt.Sprintf("Snapshot from %s failed: %v", name, err)})
			continue
		}
		content = append(content,
			&mcp.TextContent{Text: fmt.Sprintf("Snapshot from %s at %s", name, stamp(shots[i].at))},
			&mcp.ImageContent{Data: shots[i].jpeg, MIMEType: "image/jpeg"},
		)
	}
	// Only a total failure is an error; partial results are still useful.
	return &mcp.CallToolResult{Content: content, IsError: failed == len(names)}, nil, nil
}

func (t *toolset) status(ctx context.Context, _ *mcp.CallToolRequest, in StatusInput) (*mcp.CallToolResult, StatusOutput, error) {
	cams, err := t.cameras(ctx)
	if err != nil {
		return nil, StatusOutput{}, err
	}
	names, err := resolve(cams, in.Cameras)
	if err != nil {
		return nil, StatusOutput{}, err
	}
	all, err := t.src.Status(ctx)
	if err != nil {
		return nil, StatusOutput{}, err
	}
	byName := make(map[string]go2rtc.StreamStatus, len(all))
	for _, s := range all {
		byName[s.Name] = s
	}
	out := StatusOutput{Cameras: make([]CameraStatus, len(names))}
	var idle []int // cameras whose online state is unknown
	for i, name := range names {
		s := byName[name]
		out.Cameras[i] = CameraStatus{Camera: name, State: StateIdle, Viewers: s.Consumers}
		out.Cameras[i].Video, out.Cameras[i].Audio = codecs(s.Media)
		if s.Active {
			out.Cameras[i].State = StateStreaming
		} else {
			idle = append(idle, i)
		}
	}
	// A streaming camera is online by definition, so only idle ones need a test frame.
	if in.Check && len(idle) > 0 {
		probe := make([]string, len(idle))
		for j, i := range idle {
			probe[j] = names[i]
		}
		for j, s := range t.frames(ctx, probe, checkWidth) {
			out.Cameras[idle[j]].State = StateOnline
			if s.err != nil {
				out.Cameras[idle[j]].State = StateOffline
			}
		}
	}
	slog.Info("camera_status", "count", len(names), "check", in.Check, "probed", len(idle)*boolInt(in.Check))
	return nil, out, nil
}

// codecs turns go2rtc media descriptions ("video, recvonly, H264") into the codecs the camera
// sends. sendonly media (e.g. two-way audio back to the camera) is skipped.
func codecs(media []string) (video, audio string) {
	var v, a []string
	for _, m := range media {
		parts := strings.Split(m, ", ")
		if len(parts) < 3 || parts[1] != "recvonly" {
			continue
		}
		switch parts[0] {
		case "video":
			v = append(v, parts[2:]...)
		case "audio":
			for _, c := range parts[2:] {
				// go2rtc uses the RTP name for AAC; show the familiar one, keeping the sample rate.
				a = append(a, strings.Replace(c, "MPEG4-GENERIC", "AAC", 1))
			}
		}
	}
	return strings.Join(v, ", "), strings.Join(a, ", ")
}

type shot struct {
	cached
	err error
}

// frames fetches one snapshot per camera in parallel, returned in the order of names.
func (t *toolset) frames(ctx context.Context, names []string, width int) []shot {
	shots := make([]shot, len(names))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			shots[i].cached, shots[i].err = t.frame(ctx, name, width)
		}()
	}
	wg.Wait()
	return shots
}

// resolve maps requested names to known cameras, deduplicated; empty means all cameras.
// Any unknown name fails the whole request.
func resolve(cams, requested []string) ([]string, error) {
	if len(requested) == 0 {
		if len(cams) == 0 {
			return nil, fmt.Errorf("no cameras available")
		}
		return cams, nil
	}
	var names []string
	seen := map[string]bool{}
	for _, want := range requested {
		name, ok := find(cams, want)
		if !ok {
			return nil, unknownCamera(want, cams)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

// width applies the default and the cap to a requested width.
func (t *toolset) width(requested int) int {
	w := requested
	if w <= 0 {
		w = t.opt.DefaultWidth
	}
	if t.opt.MaxWidth > 0 && w > t.opt.MaxWidth {
		w = t.opt.MaxWidth
	}
	return w
}

// frame returns a snapshot via the cache, so a camera isn't asked again for a fresh frame.
func (t *toolset) frame(ctx context.Context, name string, width int) (cached, error) {
	return t.cache.get(ctx, cacheKey{name, width}, func(ctx context.Context) ([]byte, error) {
		start := time.Now()
		jpeg, err := t.src.Frame(ctx, name, width)
		if err != nil {
			// go2rtc's error text can contain camera addresses or source URLs, so it stays in the log.
			slog.Error("snapshot", "camera", name, "err", err)
			if isTimeout(err) {
				return nil, fmt.Errorf("camera %s timed out; it may be offline or slow to connect (details in the go2rtc-mcp log)", name)
			}
			return nil, fmt.Errorf("camera %s is offline or unreachable (details in the go2rtc-mcp log)", name)
		}
		slog.Info("snapshot", "camera", name, "width", width, "bytes", len(jpeg), "took", time.Since(start).Round(time.Millisecond))
		return jpeg, nil
	})
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func unknownCamera(name string, cams []string) error {
	return fmt.Errorf("unknown camera %q; available: %s", name, strings.Join(cams, ", "))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func stamp(t time.Time) string { return t.Format("2006-01-02 15:04:05 MST") }

func find(names []string, want string) (string, bool) {
	want = strings.TrimSpace(want)
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return n, true
		}
	}
	return "", false
}
