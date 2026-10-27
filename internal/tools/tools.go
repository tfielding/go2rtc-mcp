// Package tools defines the MCP tools. Everything here is read-only.
package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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

// Register adds list_cameras and get_snapshot to s.
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
		return nil, nil, fmt.Errorf("unknown camera %q; available: %s", in.Camera, strings.Join(cams, ", "))
	}

	width := in.Width
	if width <= 0 {
		width = t.opt.DefaultWidth
	}
	if t.opt.MaxWidth > 0 && width > t.opt.MaxWidth {
		width = t.opt.MaxWidth
	}

	start := time.Now()
	jpeg, err := t.src.Frame(ctx, name, width)
	if err != nil {
		slog.Error("get_snapshot", "camera", name, "err", err)
		return nil, nil, err
	}
	slog.Info("get_snapshot", "camera", name, "width", width, "bytes", len(jpeg), "took", time.Since(start).Round(time.Millisecond))
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: fmt.Sprintf("Snapshot from %s at %s", name, time.Now().Format("2006-01-02 15:04:05 MST"))},
		&mcp.ImageContent{Data: jpeg, MIMEType: "image/jpeg"},
	}}, nil, nil
}

func find(names []string, want string) (string, bool) {
	want = strings.TrimSpace(want)
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return n, true
		}
	}
	return "", false
}
