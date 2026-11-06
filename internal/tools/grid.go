package tools

// get_snapshots as one picture: every camera in a numbered grid, so a model that handles one image
// well (or has little context) sees all cameras at once. The drawing follows frigate-mcp's grids.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	gridWidth    = 1280 // default width of the whole grid (16:9)
	gridMinWidth = 640
	gridGap      = 4
)

var (
	gridBackground  = color.RGBA{24, 24, 24, 255}
	labelBackground = color.RGBA{0, 0, 0, 200}
	labelText       = color.RGBA{255, 220, 0, 255}
)

// Corner is where tile labels go. The default is bottom-left, because cameras usually draw their
// own timestamp top-left.
type Corner string

const (
	TopLeft     Corner = "top-left"
	TopRight    Corner = "top-right"
	BottomLeft  Corner = "bottom-left"
	BottomRight Corner = "bottom-right"
	center      Corner = "center"
)

// ParseCorner reads a label position: top-left, top-right, bottom-left or bottom-right (also
// tl, tr, bl, br, with spaces or underscores). Empty means bottom-left.
func ParseCorner(s string) (Corner, error) {
	switch strings.NewReplacer(" ", "-", "_", "-").Replace(strings.ToLower(strings.TrimSpace(s))) {
	case "", "bottom-left", "bl":
		return BottomLeft, nil
	case "top-left", "tl":
		return TopLeft, nil
	case "top-right", "tr":
		return TopRight, nil
	case "bottom-right", "br":
		return BottomRight, nil
	}
	return "", fmt.Errorf("label position %q not understood; use top-left, top-right, bottom-left or bottom-right", s)
}

// layout is a grid of 16:9 tiles that fits within maxW pixels wide and maxW*9/16 tall.
type layout struct {
	cols, rows   int
	cellW, cellH int
	corner       Corner // where labels go; empty = bottom-left
}

func newLayout(n, maxW int) layout {
	cols := int(math.Ceil(math.Sqrt(float64(n))))
	rows := (n + cols - 1) / cols
	maxH := maxW * 9 / 16
	cellW := min((maxW-gridGap*(cols-1))/cols, ((maxH-gridGap*(rows-1))/rows)*16/9)
	return layout{cols: cols, rows: rows, cellW: cellW, cellH: cellW * 9 / 16}
}

func (l layout) size() (int, int) {
	return l.cols*l.cellW + gridGap*(l.cols-1), l.rows*l.cellH + gridGap*(l.rows-1)
}

// render draws each picture scaled to fit its tile (centered, keeping its shape) with its label,
// and encodes the grid as JPEG. A nil picture gets an empty tile saying missing.
func (l layout) render(pics []image.Image, labels []string, missing string) ([]byte, error) {
	w, h := l.size()
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(gridBackground), image.Point{}, draw.Src)
	for i, pic := range pics {
		x0 := (i % l.cols) * (l.cellW + gridGap)
		y0 := (i / l.cols) * (l.cellH + gridGap)
		cell := image.Rect(x0, y0, x0+l.cellW, y0+l.cellH)
		if pic != nil {
			draw.ApproxBiLinear.Scale(canvas, fit(pic.Bounds(), cell), pic, pic.Bounds(), draw.Src, nil)
		} else {
			drawLabel(canvas, cell, missing, center)
		}
		drawLabel(canvas, cell, labels[i], l.corner)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, canvas, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// fit returns the largest rectangle with src's shape that fits centered in cell.
func fit(src, cell image.Rectangle) image.Rectangle {
	sw, sh := src.Dx(), src.Dy()
	if sw == 0 || sh == 0 {
		return cell
	}
	w, h := cell.Dx(), cell.Dx()*sh/sw
	if h > cell.Dy() {
		w, h = cell.Dy()*sw/sh, cell.Dy()
	}
	x, y := cell.Min.X+(cell.Dx()-w)/2, cell.Min.Y+(cell.Dy()-h)/2
	return image.Rect(x, y, x+w, y+h)
}

// drawLabel writes s on a dark band in a corner (or the center) of cell, clipped to cell. The
// built-in 7x13 font is enlarged with the tile so a vision model can read it. A label too long for
// the tile is cut off at the end, so its number stays visible.
func drawLabel(dst *image.RGBA, cell image.Rectangle, s string, where Corner) {
	face := basicfont.Face7x13
	pad := 3
	tw := font.MeasureString(face, s).Ceil() + 2*pad
	th := face.Height + 2*pad
	small := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(small, small.Bounds(), image.NewUniform(labelBackground), image.Point{}, draw.Src)
	d := font.Drawer{Dst: small, Src: image.NewUniform(labelText), Face: face, Dot: fixed.P(pad, pad+face.Ascent)}
	d.DrawString(s)

	scale := max(1, cell.Dy()/160)
	for scale > 1 && tw*scale > cell.Dx() {
		scale--
	}
	w, h := tw*scale, th*scale
	left, right := cell.Min.X, max(cell.Min.X, cell.Max.X-w)
	top, bottom := cell.Min.Y, cell.Max.Y-h
	pos := image.Pt(left, bottom)
	switch where {
	case TopLeft:
		pos = image.Pt(left, top)
	case TopRight:
		pos = image.Pt(right, top)
	case BottomRight:
		pos = image.Pt(right, bottom)
	case center:
		pos = image.Pt(max(left, cell.Min.X+(cell.Dx()-w)/2), cell.Min.Y+(cell.Dy()-h)/2)
	}
	clip := dst.SubImage(cell).(*image.RGBA)
	draw.NearestNeighbor.Scale(clip, image.Rect(pos.X, pos.Y, pos.X+w, pos.Y+h), small, small.Bounds(), draw.Over, nil)
}
