package main

import "testing"

func TestCameraValidate(t *testing.T) {
	base := func() Camera {
		return Camera{
			Name:      "Front door",
			RTSPURL:   "rtsp://192.168.1.10:554/live",
			Transport: TransportAuto,
			Recording: RecordingSettings{Mode: RecordingOff},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Camera)
		wantErr bool
	}{
		{"valid", func(c *Camera) {}, false},
		{"missing name", func(c *Camera) { c.Name = "" }, true},
		{"missing url", func(c *Camera) { c.RTSPURL = "" }, true},
		{"wrong scheme", func(c *Camera) { c.RTSPURL = "http://192.168.1.10/live" }, true},
		{"no host", func(c *Camera) { c.RTSPURL = "rtsp:///live" }, true},
		{"rtsps ok", func(c *Camera) { c.RTSPURL = "rtsps://192.168.1.10/live" }, false},
		{"bad transport", func(c *Camera) { c.Transport = "quic" }, true},
		{"bad codec", func(c *Camera) { c.Codec = "vp9" }, true},
		{"negative fps", func(c *Camera) { c.FPS = -1 }, true},
		{"huge fps", func(c *Camera) { c.FPS = 5000 }, true},
		{"bad recording mode", func(c *Camera) { c.Recording.Mode = "always" }, true},
		{"bad recording format", func(c *Camera) { c.Recording.Format = "avi" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mutate(&c)
			c.normalise()
			err := c.validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCameraNormalise(t *testing.T) {
	c := Camera{
		Name:      "  Cam  ",
		RTSPURL:   " rtsp://host/live ",
		Transport: "TCP",
		Codec:     "H.264",
		Tags:      []string{"Outdoor", "outdoor", " hd ", ""},
	}
	c.normalise()
	if c.Name != "Cam" {
		t.Errorf("name not trimmed: %q", c.Name)
	}
	if c.Transport != "tcp" {
		t.Errorf("transport not lowered: %q", c.Transport)
	}
	if c.Codec != "h264" {
		t.Errorf("codec not folded: %q", c.Codec)
	}
	if len(c.Tags) != 2 {
		t.Errorf("tags not deduped/cleaned: %v", c.Tags)
	}
	if c.Recording.Mode != RecordingOff {
		t.Errorf("recording mode default not applied: %q", c.Recording.Mode)
	}
}

func TestConnectionURL(t *testing.T) {
	tests := []struct {
		name string
		cam  Camera
		want string
	}{
		{
			"embeds separate creds",
			Camera{RTSPURL: "rtsp://192.168.1.10/live", Username: "admin", Password: "secret"},
			"rtsp://admin:secret@192.168.1.10/live",
		},
		{
			"keeps existing userinfo",
			Camera{RTSPURL: "rtsp://u:p@192.168.1.10/live", Username: "other", Password: "x"},
			"rtsp://u:p@192.168.1.10/live",
		},
		{
			"username only",
			Camera{RTSPURL: "rtsp://192.168.1.10/live", Username: "admin"},
			"rtsp://admin@192.168.1.10/live",
		},
		{
			"no creds",
			Camera{RTSPURL: "rtsp://192.168.1.10/live"},
			"rtsp://192.168.1.10/live",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cam.connectionURL()
			if err != nil {
				t.Fatalf("connectionURL() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("connectionURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGroupValidate(t *testing.T) {
	tests := []struct {
		name    string
		group   Group
		wantErr bool
	}{
		{"valid building", Group{Name: "HQ", Kind: "building"}, false},
		{"valid no kind", Group{Name: "Misc"}, false},
		{"empty name", Group{Name: "   "}, true},
		{"bad kind", Group{Name: "HQ", Kind: "planet"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := tt.group
			if err := g.validate(); (err != nil) != tt.wantErr {
				t.Fatalf("validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
