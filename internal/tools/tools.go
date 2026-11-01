// Package tools defines the MCP tools. Everything here is read-only.
package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Source is the camera backend (implemented by go2rtc.Client).
type Source interface {
	Streams(ctx context.Context) ([]string, error)
	Frame(ctx context.Context, stream string, width int) ([]byte, error)
}

type Options struct {
	Allowed      map[string]bool // lowercased camera names; empty = all cameras
	DefaultWidth int             // snapshot width in pixels; 0 = native
	MaxWidth     int             // cap on requested width; 0 = no cap
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

// maxParallel limits concurrent frame requests to go2rtc in get_snapshots.
const maxParallel = 4

// Register adds list_cameras, get_snapshot and get_snapshots to s.
func Register(s *mcp.Server, src Source, opt Options) {
	t := &toolset{src: src, opt: opt}
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
}

func readOnly(title string) *mcp.ToolAnnotations {
	f := false
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, DestructiveHint: &f, OpenWorldHint: &f}
}

type toolset struct {
	src Source
	opt Options
}

func (t *toolset) cameras(ctx context.Context) ([]string, error) {
	all, err := t.src.Streams(ctx)
	if err != nil {
		return nil, err
	}
	if len(t.opt.Allowed) == 0 {
		return all, nil
	}
	var out []string
	for _, name := range all {
		if t.opt.Allowed[strings.ToLower(name)] {
			out = append(out, name)
		}
	}
	return out, nil
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
	jpeg, err := t.frame(ctx, name, width)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: fmt.Sprintf("Snapshot from %s at %s", name, now())},
		&mcp.ImageContent{Data: jpeg, MIMEType: "image/jpeg"},
	}}, nil, nil
}

func (t *toolset) snapshots(ctx context.Context, _ *mcp.CallToolRequest, in SnapshotsInput) (*mcp.CallToolResult, any, error) {
	cams, err := t.cameras(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Resolve every name before contacting go2rtc, so one bad name sends no requests at all.
	names := cams
	if len(in.Cameras) > 0 {
		names = nil
		seen := map[string]bool{}
		for _, want := range in.Cameras {
			name, ok := find(cams, want)
			if !ok {
				return nil, nil, unknownCamera(want, cams)
			}
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("no cameras available")
	}

	width := t.width(in.Width)
	type shot struct {
		jpeg []byte
		err  error
	}
	shots := make([]shot, len(names))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			shots[i].jpeg, shots[i].err = t.frame(ctx, name, width)
		}()
	}
	wg.Wait()

	ts := now()
	content := []mcp.Content{}
	failed := 0
	for i, name := range names {
		if err := shots[i].err; err != nil {
			failed++
			content = append(content, &mcp.TextContent{Text: fmt.Sprintf("Snapshot from %s failed: %v", name, err)})
			continue
		}
		content = append(content,
			&mcp.TextContent{Text: fmt.Sprintf("Snapshot from %s at %s", name, ts)},
			&mcp.ImageContent{Data: shots[i].jpeg, MIMEType: "image/jpeg"},
		)
	}
	// Only a total failure is an error; partial results are still useful.
	return &mcp.CallToolResult{Content: content, IsError: failed == len(names)}, nil, nil
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

// frame fetches one snapshot and logs the outcome.
func (t *toolset) frame(ctx context.Context, name string, width int) ([]byte, error) {
	start := time.Now()
	jpeg, err := t.src.Frame(ctx, name, width)
	if err != nil {
		slog.Error("snapshot", "camera", name, "err", err)
		return nil, err
	}
	slog.Info("snapshot", "camera", name, "width", width, "bytes", len(jpeg), "took", time.Since(start).Round(time.Millisecond))
	return jpeg, nil
}

func unknownCamera(name string, cams []string) error {
	return fmt.Errorf("unknown camera %q; available: %s", name, strings.Join(cams, ", "))
}

func now() string { return time.Now().Format("2006-01-02 15:04:05 MST") }

func find(names []string, want string) (string, bool) {
	want = strings.TrimSpace(want)
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return n, true
		}
	}
	return "", false
}
