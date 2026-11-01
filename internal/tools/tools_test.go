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
