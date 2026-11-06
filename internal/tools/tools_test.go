package tools

import (
	"strings"
	"testing"
)

func TestCodecs(t *testing.T) {
	for _, tc := range []struct {
		media        []string
		video, audio string
	}{
		{[]string{"video, recvonly, H264"}, "H264", ""},                                                  // Hikvision substream
		{[]string{"video, recvonly, H265", "audio, recvonly, MPEG4-GENERIC/16000"}, "H265", "AAC/16000"}, // Reolink
		{[]string{"video, recvonly, H264", "audio, recvonly, PCMA/8000", "audio, sendonly, PCMU/8000"}, "H264", "PCMA/8000"},
		{[]string{"weird"}, "", ""},
		{nil, "", ""},
	} {
		v, a := codecs(tc.media)
		if v != tc.video || a != tc.audio {
			t.Errorf("%s: got %q/%q, want %q/%q", strings.Join(tc.media, " | "), v, a, tc.video, tc.audio)
		}
	}
}

func TestParseCorner(t *testing.T) {
	for in, want := range map[string]Corner{"": BottomLeft, "TR": TopRight, "top_left": TopLeft, "bottom right": BottomRight} {
		if got, err := ParseCorner(in); err != nil || got != want {
			t.Errorf("ParseCorner(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseCorner("middle"); err == nil {
		t.Error("middle accepted")
	}
}

func TestGridLayout(t *testing.T) {
	for _, tc := range []struct{ n, maxW, w, h int }{
		{1, 1280, 1280, 720}, {2, 1280, 1280, 358}, {3, 1280, 1276, 718}, {4, 1280, 1276, 718}, {9, 1280, 1271, 716},
	} {
		l := newLayout(tc.n, tc.maxW)
		if w, h := l.size(); w != tc.w || h != tc.h || w > tc.maxW || h > tc.maxW*9/16 {
			t.Errorf("%d cameras in %d: %dx%d, want %dx%d", tc.n, tc.maxW, w, h, tc.w, tc.h)
		}
	}
}
