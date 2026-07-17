package main

import (
	"testing"
	"time"
)

func TestValidateStreamBadInput(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"empty", "", "invalid_url"},
		{"wrong scheme", "http://host/live", "invalid_url"},
		{"garbage", "://::", "invalid_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := validateStream(tt.url, "", "", time.Second)
			if res.Error != tt.wantErr {
				t.Errorf("Error = %q, want %q", res.Error, tt.wantErr)
			}
			if res.OK {
				t.Error("OK should be false for bad input")
			}
		})
	}
}

func TestValidateStreamUnreachable(t *testing.T) {
	// TEST-NET-1 (192.0.2.0/24, RFC 5737) is guaranteed non-routable, so this
	// exercises the unreachable / timeout branch without hitting a real host.
	res := validateStream("rtsp://192.0.2.1:554/live", "", "", 300*time.Millisecond)
	if res.Reachable {
		t.Error("expected unreachable host")
	}
	if res.Error != "timeout" && res.Error != "unreachable" {
		t.Errorf("Error = %q, want timeout or unreachable", res.Error)
	}
}

func TestParseSDP(t *testing.T) {
	sdp := "v=0\r\n" +
		"m=video 0 RTP/AVP 96\r\n" +
		"a=rtpmap:96 H264/90000\r\n" +
		"a=framerate:25\r\n" +
		"a=x-dimensions:1920,1080\r\n"
	codec, res, fps := parseSDP(sdp)
	if codec != "h264" {
		t.Errorf("codec = %q, want h264", codec)
	}
	if res != "1920x1080" {
		t.Errorf("resolution = %q, want 1920x1080", res)
	}
	if fps != 25 {
		t.Errorf("fps = %d, want 25", fps)
	}
}

func TestNormaliseCodecName(t *testing.T) {
	cases := map[string]string{
		"H264": "h264", "HEVC": "h265", "H265": "h265",
		"JPEG": "mjpeg", "AV1": "av1", "MP4V-ES": "mpeg4", "OPUS": "",
	}
	for in, want := range cases {
		if got := normaliseCodecName(in); got != want {
			t.Errorf("normaliseCodecName(%q) = %q, want %q", in, got, want)
		}
	}
}
