package ffmpegutil

import (
	"strings"
	"testing"
)

// minimalSpec returns a valid one-video-clip spec that tests mutate.
func minimalSpec() *TimelineSpec {
	return &TimelineSpec{
		Width:    1280,
		Height:   720,
		FPS:      30,
		Duration: 10,
		Sources: []TimelineSource{
			{ID: "s0", Path: "/tmp/in.mp4", Type: "video"},
		},
		Video: []TimelineVideoClip{
			{Source: "s0", Start: 0, In: 0, Out: 10, Speed: 1, Scale: 100, Opacity: 100, Crop: "fit", Saturation: 1, Preset: "default"},
		},
	}
}

func TestParseTimelineSpec(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name: "valid minimal spec",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"}],
				"video":[{"source":"a","start":0,"in":0,"out":5}]}`,
			wantErr: false,
		},
		{
			name:    "broken json",
			json:    `{"width":`,
			wantErr: true,
		},
		{
			name: "unknown source reference",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"}],
				"video":[{"source":"nope","start":0,"in":0,"out":5}]}`,
			wantErr: true,
		},
		{
			name: "audio clip on image source",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.png","type":"image"}],
				"audio":[{"source":"a","start":0,"in":0,"out":5}]}`,
			wantErr: true,
		},
		{
			name: "video clip on audio source",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.mp3","type":"audio"}],
				"video":[{"source":"a","start":0,"in":0,"out":5}]}`,
			wantErr: true,
		},
		{
			name: "oversized canvas",
			json: `{"width":100000,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"}],
				"video":[{"source":"a","start":0,"in":0,"out":5}]}`,
			wantErr: true,
		},
		{
			name: "zero duration",
			json: `{"width":1280,"height":720,"fps":30,"duration":0,
				"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"}],
				"video":[{"source":"a","start":0,"in":0,"out":5}]}`,
			wantErr: true,
		},
		{
			name: "in after out",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"}],
				"video":[{"source":"a","start":0,"in":5,"out":1}]}`,
			wantErr: true,
		},
		{
			name: "speed out of range",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"}],
				"video":[{"source":"a","start":0,"in":0,"out":5,"speed":100}]}`,
			wantErr: true,
		},
		{
			name: "bad source id",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"../a","vpath":"/tmp/a.mp4","type":"video"}],
				"video":[{"source":"../a","start":0,"in":0,"out":5}]}`,
			wantErr: true,
		},
		{
			name: "duplicated source id",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"},
					{"id":"a","vpath":"/tmp/b.mp4","type":"video"}],
				"video":[{"source":"a","start":0,"in":0,"out":5}]}`,
			wantErr: true,
		},
		{
			name: "unsupported crop mode",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"}],
				"video":[{"source":"a","start":0,"in":0,"out":5,"crop":"tile"}]}`,
			wantErr: true,
		},
		{
			name: "no clips at all",
			json: `{"width":1280,"height":720,"fps":30,"duration":5,
				"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"}]}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTimelineSpec(tt.json)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTimelineSpec() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseTimelineSpecDefaults(t *testing.T) {
	spec, err := ParseTimelineSpec(`{"width":1281,"height":721,"fps":0,"duration":5,
		"sources":[{"id":"a","vpath":"/tmp/a.mp4","type":"video"}],
		"video":[{"source":"a","start":0,"in":0,"out":5}],
		"audio":[{"source":"a","start":0,"in":0,"out":5}]}`)
	if err != nil {
		t.Fatalf("ParseTimelineSpec() unexpected error: %v", err)
	}
	if spec.Width != 1280 || spec.Height != 720 {
		t.Errorf("odd dimensions not rounded to even: got %dx%d", spec.Width, spec.Height)
	}
	if spec.FPS != 30 {
		t.Errorf("fps default = %v, want 30", spec.FPS)
	}
	v := spec.Video[0]
	if v.Speed != 1 || v.Scale != 100 || v.Opacity != 100 || v.Crop != "fit" || v.Preset != "default" {
		t.Errorf("video clip defaults not applied: %+v", v)
	}
	if spec.Audio[0].Speed != 1 || spec.Audio[0].Volume != 100 {
		t.Errorf("audio clip defaults not applied: %+v", spec.Audio[0])
	}
}

func TestAtempoChain(t *testing.T) {
	tests := []struct {
		name  string
		speed float64
		want  []string
	}{
		{name: "normal speed", speed: 1, want: []string{}},
		{name: "in range", speed: 1.5, want: []string{"atempo=1.5"}},
		{name: "double", speed: 2, want: []string{"atempo=2"}},
		{name: "quadruple", speed: 4, want: []string{"atempo=2", "atempo=2"}},
		{name: "six times", speed: 6, want: []string{"atempo=2", "atempo=2", "atempo=1.5"}},
		{name: "half", speed: 0.5, want: []string{"atempo=0.5"}},
		{name: "quarter", speed: 0.25, want: []string{"atempo=0.5", "atempo=0.5"}},
		{name: "sixteenth", speed: 0.0625, want: []string{"atempo=0.5", "atempo=0.5", "atempo=0.5", "atempo=0.5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := atempoChain(tt.speed)
			if len(got) != len(tt.want) {
				t.Fatalf("atempoChain(%v) = %v, want %v", tt.speed, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("atempoChain(%v)[%d] = %q, want %q", tt.speed, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestEffectFilters(t *testing.T) {
	tests := []struct {
		name         string
		effect       TimelineEffect
		wantContains []string
		wantEmpty    bool
	}{
		{name: "black and white", effect: TimelineEffect{Type: "bw", Amount: 100}, wantContains: []string{"hue=s=0"}},
		{name: "half grayscale", effect: TimelineEffect{Type: "bw", Amount: 50}, wantContains: []string{"hue=s=0.5"}},
		{name: "sepia", effect: TimelineEffect{Type: "sepia", Amount: 100}, wantContains: []string{"colorchannelmixer=", "rr=0.393"}},
		{name: "full invert", effect: TimelineEffect{Type: "invert", Amount: 100}, wantContains: []string{"lutrgb=", "255+val*-1"}},
		{name: "hue shift", effect: TimelineEffect{Type: "hue", Amount: 90}, wantContains: []string{"hue=h=90"}},
		{name: "blur", effect: TimelineEffect{Type: "blur", Amount: 6}, wantContains: []string{"gblur=sigma=3"}},
		{name: "pixelate", effect: TimelineEffect{Type: "pixelate", Amount: 12}, wantContains: []string{"trunc(iw/12)", "flags=neighbor"}},
		{name: "vignette", effect: TimelineEffect{Type: "vignette", Amount: 100}, wantContains: []string{"vignette=angle="}},
		{name: "grain", effect: TimelineEffect{Type: "grain", Amount: 50}, wantContains: []string{"noise=alls=12"}},
		{name: "zero blur skipped", effect: TimelineEffect{Type: "blur", Amount: 0}, wantEmpty: true},
		{name: "unknown effect skipped", effect: TimelineEffect{Type: "wobble", Amount: 10}, wantEmpty: true},
		{name: "fade handled elsewhere", effect: TimelineEffect{Type: "fadein", Amount: 1}, wantEmpty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(effectFilters(tt.effect, 1.0), ",")
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("effectFilters(%v) = %q, want empty", tt.effect, got)
				}
				return
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("effectFilters(%v) = %q, missing %q", tt.effect, got, want)
				}
			}
		})
	}
}

func TestBuildTimelineArgsVideo(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*TimelineSpec)
		ext          string
		wantErr      bool
		wantContains []string
		wantabsent   []string
	}{
		{
			name:   "mp4 encoder settings",
			mutate: func(s *TimelineSpec) {},
			ext:    ".mp4",
			wantContains: []string{
				"libx264", "aac", "+faststart",
				"[vout]", "[aout]", "anullsrc",
				"color=c=black:s=1280x720:r=30:d=10",
			},
		},
		{
			name:         "webm encoder settings",
			mutate:       func(s *TimelineSpec) {},
			ext:          ".webm",
			wantContains: []string{"libvpx-vp9", "libopus"},
			wantabsent:   nil,
		},
		{
			name:    "unsupported container",
			mutate:  func(s *TimelineSpec) {},
			ext:     ".avi",
			wantErr: true,
		},
		{
			name: "image source gets loop input flags",
			mutate: func(s *TimelineSpec) {
				s.Sources[0] = TimelineSource{ID: "s0", Path: "/tmp/in.png", Type: "image"}
			},
			ext:          ".mp4",
			wantContains: []string{"-loop", "trim=duration=10"},
		},
		{
			name: "speed change rescales pts",
			mutate: func(s *TimelineSpec) {
				s.Video[0].Speed = 2
			},
			ext:          ".mp4",
			wantContains: []string{"setpts=(PTS-STARTPTS)/2"},
		},
		{
			name: "transition freeze and alpha ramp",
			mutate: func(s *TimelineSpec) {
				s.Video[0].Extend = 1
				s.Video[0].ExtendFadeOut = 0.5
				s.Video[0].TransFadeIn = 1
				s.Video[0].TransFadeInOffset = 0.5
			},
			ext: ".mp4",
			wantContains: []string{
				"tpad=stop_mode=clone:stop_duration=1",
				"fade=t=in:st=0.5:d=1:alpha=1",
				"fade=t=out:st=10.5:d=0.5:alpha=1",
			},
		},
		{
			name: "transform and colour pipeline",
			mutate: func(s *TimelineSpec) {
				s.Video[0].X = 10
				s.Video[0].Y = -20
				s.Video[0].Rotation = 45
				s.Video[0].Opacity = 50
				s.Video[0].FlipV = true
				s.Video[0].Exposure = 0.2
				s.Video[0].Contrast = 10
				s.Video[0].Saturation = 1.5
				s.Video[0].Preset = "cool"
			},
			ext: ".mp4",
			wantContains: []string{
				"rotate=a=0.785398",
				"colorchannelmixer=aa=0.5",
				"vflip",
				"colorchannelmixer=rr=1.2:gg=1.2:bb=1.2",
				"eq=contrast=1.1:saturation=1.5",
				"hue=h=-18",
				"(main_w-overlay_w)/2+(10)",
				"(main_h-overlay_h)/2+(-20)",
			},
		},
		{
			name: "colour filters restore the alpha plane",
			mutate: func(s *TimelineSpec) {
				s.Video[0].Contrast = 10
				s.Video[0].Opacity = 50
			},
			ext: ".mp4",
			wantContains: []string{
				"eq=contrast=1.1,format=rgba,colorchannelmixer=aa=0.5",
			},
		},
		{
			name: "clean clip has no redundant format stage",
			mutate: func(s *TimelineSpec) {
				s.Video[0].Opacity = 50
			},
			ext:        ".mp4",
			wantabsent: []string{"format=rgba,colorchannelmixer=aa"},
		},
		{
			name: "mirror effect cancels flipH",
			mutate: func(s *TimelineSpec) {
				s.Video[0].FlipH = true
				s.Video[0].Effects = []TimelineEffect{{Type: "mirror"}}
			},
			ext:        ".mp4",
			wantabsent: []string{"hflip"},
		},
		{
			name: "stretch crop uses fixed size",
			mutate: func(s *TimelineSpec) {
				s.Video[0].Crop = "stretch"
				s.Video[0].Scale = 50
			},
			ext:          ".mp4",
			wantContains: []string{"scale=w=640:h=360"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := minimalSpec()
			tt.mutate(spec)
			args, err := buildTimelineArgs(spec, tt.ext)
			if (err != nil) != tt.wantErr {
				t.Fatalf("buildTimelineArgs() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			joined := strings.Join(args, " ")
			for _, want := range tt.wantContains {
				if !strings.Contains(joined, want) {
					t.Errorf("args missing %q in: %s", want, joined)
				}
			}
			for _, absent := range tt.wantabsent {
				if strings.Contains(joined, absent) {
					t.Errorf("args unexpectedly contain %q in: %s", absent, joined)
				}
			}
		})
	}
}

func TestBuildTimelineArgsAudio(t *testing.T) {
	spec := minimalSpec()
	spec.Sources = append(spec.Sources, TimelineSource{ID: "s1", Path: "/tmp/in.mp3", Type: "audio"})
	spec.Audio = []TimelineAudioClip{
		{Source: "s0", Start: 0, In: 0, Out: 10, Speed: 1, Volume: 100},
		{Source: "s1", Start: 2.5, In: 1, Out: 5, Speed: 4, Volume: 50, FadeIn: 0.5, FadeOut: 1},
	}
	args, err := buildTimelineArgs(spec, ".mp4")
	if err != nil {
		t.Fatalf("buildTimelineArgs() unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"amix=inputs=2:duration=longest:dropout_transition=0:normalize=0",
		"adelay=2500:all=1",
		"atempo=2,atempo=2",
		"volume=0.5",
		"afade=t=in:st=0:d=0.5",
		"afade=t=out:st=0:d=1", // visible duration (5-1)/4 = 1s, so fade-out starts at 0
		"atrim=start=1:end=5",
		"aformat=sample_fmts=fltp:sample_rates=44100:channel_layouts=stereo",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("audio args missing %q in: %s", want, joined)
		}
	}
	if strings.Contains(joined, "anullsrc") {
		t.Errorf("anullsrc should not be present when audio clips exist")
	}
}

func TestBuildTimelineArgsSingleAudioSkipsAmix(t *testing.T) {
	spec := minimalSpec()
	spec.Audio = []TimelineAudioClip{
		{Source: "s0", Start: 0, In: 0, Out: 10, Speed: 1, Volume: 100},
	}
	args, err := buildTimelineArgs(spec, ".mp4")
	if err != nil {
		t.Fatalf("buildTimelineArgs() unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "amix") {
		t.Errorf("single audio clip should not use amix: %s", joined)
	}
	if !strings.Contains(joined, "atrim=duration=10[aout]") {
		t.Errorf("single audio clip should be trimmed to the timeline duration: %s", joined)
	}
}

func TestBuildTimelineArgsInputPerClip(t *testing.T) {
	spec := minimalSpec()
	spec.Video = append(spec.Video, TimelineVideoClip{
		Source: "s0", Start: 5, In: 2, Out: 4, Speed: 1, Scale: 100, Opacity: 100, Crop: "fit", Saturation: 1, Preset: "default",
	})
	args, err := buildTimelineArgs(spec, ".mp4")
	if err != nil {
		t.Fatalf("buildTimelineArgs() unexpected error: %v", err)
	}
	inputs := 0
	for i, a := range args {
		if a == "-i" && i+1 < len(args) && args[i+1] == "/tmp/in.mp4" {
			inputs++
		}
	}
	if inputs != 2 {
		t.Errorf("expected the shared source to be opened once per clip (2), got %d", inputs)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "[0:v]") || !strings.Contains(joined, "[1:v]") {
		t.Errorf("expected chains for both inputs in: %s", joined)
	}
	if !strings.Contains(joined, "[bg2]format=yuv420p[vout]") {
		t.Errorf("expected two overlay stages ending in [bg2]: %s", joined)
	}
}

func TestSepiaAndInvertCoefficients(t *testing.T) {
	if got := sepiaMix(0); got != "colorchannelmixer=rr=1:rg=0:rb=0:gr=0:gg=1:gb=0:br=0:bg=0:bb=1" {
		t.Errorf("sepiaMix(0) should be the identity matrix, got %q", got)
	}
	if got := sepiaMix(1); !strings.Contains(got, "rr=0.393") || !strings.Contains(got, "bb=0.131") {
		t.Errorf("sepiaMix(1) should be the full sepia matrix, got %q", got)
	}
	if got := invertMix(0.5); !strings.Contains(got, "127.5+val*0") {
		t.Errorf("invertMix(0.5) should collapse to mid grey, got %q", got)
	}
}
