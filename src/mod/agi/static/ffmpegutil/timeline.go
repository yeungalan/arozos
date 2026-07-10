package ffmpegutil

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

/*
	Timeline renderer for ffmpegutil

	Renders a multi-track video-editor timeline (as produced by the
	Cine Studio web app) into a single video file with one ffmpeg
	invocation. The timeline arrives as a JSON "render spec": a list
	of media sources plus video clips (composited bottom-up with
	overlay) and audio clips (mixed with amix). The spec is fully
	structured and validated here so AGI scripts never get to pass
	raw ffmpeg arguments or filter strings.
*/

// Hard limits protecting the host from oversized render requests
const (
	timelineMaxSources  = 128
	timelineMaxClips    = 256
	timelineMaxDuration = 4 * 3600 // seconds
	timelineMaxWidth    = 7680
	timelineMaxHeight   = 4320
)

// TimelineSource is one media file referenced by clips. Path holds a
// virtual path in the incoming JSON; the AGI layer rewrites it to a
// buffered local file path before rendering.
type TimelineSource struct {
	ID   string `json:"id"`
	Path string `json:"vpath"`
	Type string `json:"type"` // video | image | audio
}

// TimelineEffect is one entry of a clip's visual effect stack.
type TimelineEffect struct {
	Type   string  `json:"type"`
	Amount float64 `json:"amount"`
}

// TimelineVideoClip is a clip painted onto the canvas. Clips are
// composited in array order (earlier entries are painted first).
type TimelineVideoClip struct {
	Source string  `json:"source"`
	Start  float64 `json:"start"` // position on the timeline (s)
	In     float64 `json:"in"`    // source in point (s)
	Out    float64 `json:"out"`   // source out point (s)
	Speed  float64 `json:"speed"` // playback rate, 1 = normal

	X        float64 `json:"x"`        // offset from canvas centre (px)
	Y        float64 `json:"y"`        // offset from canvas centre (px)
	Scale    float64 `json:"scale"`    // percent, 100 = crop-mode size
	Rotation float64 `json:"rotation"` // degrees clockwise
	Opacity  float64 `json:"opacity"`  // percent
	Crop     string  `json:"crop"`     // fit | fill | stretch
	FlipH    bool    `json:"flipH"`
	FlipV    bool    `json:"flipV"`

	Exposure   float64 `json:"exposure"`   // -1 .. 1, multiplies brightness
	Contrast   float64 `json:"contrast"`   // -100 .. 100
	Saturation float64 `json:"saturation"` // 0 .. 3, 1 = unchanged
	Preset     string  `json:"preset"`     // default | warm | cool

	Effects []TimelineEffect `json:"effects"`

	FadeIn  float64 `json:"fadeIn"`  // alpha fade-in duration (s)
	FadeOut float64 `json:"fadeOut"` // alpha fade-out duration (s)

	// Transition mechanics, precomputed by the caller:
	TransFadeIn       float64 `json:"transFadeIn"`       // incoming alpha ramp (s)
	TransFadeInOffset float64 `json:"transFadeInOffset"` // ramp start offset (s)
	Extend            float64 `json:"extend"`            // freeze last frame for N s
	ExtendFadeOut     float64 `json:"extendFadeOut"`     // alpha fade-out at extended end (s)
}

// TimelineAudioClip is one audio contribution to the final mix.
type TimelineAudioClip struct {
	Source  string  `json:"source"`
	Start   float64 `json:"start"`
	In      float64 `json:"in"`
	Out     float64 `json:"out"`
	Speed   float64 `json:"speed"`
	Volume  float64 `json:"volume"` // percent, 100 = unchanged
	FadeIn  float64 `json:"fadeIn"`
	FadeOut float64 `json:"fadeOut"`
}

// TimelineSpec is the whole render job.
type TimelineSpec struct {
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	FPS      float64 `json:"fps"`
	Duration float64 `json:"duration"`
	Quality  int     `json:"quality"` // 0 = encoder default, 1-100 mapped to CRF

	Sources []TimelineSource    `json:"sources"`
	Video   []TimelineVideoClip `json:"video"`
	Audio   []TimelineAudioClip `json:"audio"`
}

var timelineSourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)

// ParseTimelineSpec unmarshals, normalizes and validates a render spec.
func ParseTimelineSpec(specJSON string) (*TimelineSpec, error) {
	spec := TimelineSpec{}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return nil, fmt.Errorf("invalid render spec: %v", err)
	}
	normalizeTimelineSpec(&spec)
	if err := validateTimelineSpec(&spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

// normalizeTimelineSpec fills sensible defaults for omitted fields so
// hand-written specs stay short.
func normalizeTimelineSpec(spec *TimelineSpec) {
	// Encoders need even frame dimensions
	spec.Width -= spec.Width % 2
	spec.Height -= spec.Height % 2
	if spec.FPS <= 0 {
		spec.FPS = 30
	}
	for i := range spec.Video {
		c := &spec.Video[i]
		if c.Speed <= 0 {
			c.Speed = 1
		}
		if c.Scale <= 0 {
			c.Scale = 100
		}
		if c.Opacity <= 0 && c.Opacity != 0 {
			c.Opacity = 0
		}
		if c.Opacity == 0 {
			c.Opacity = 100 // omitted opacity means fully visible
		}
		if c.Crop == "" {
			c.Crop = "fit"
		}
		if c.Saturation < 0 {
			c.Saturation = 0
		}
		if c.Preset == "" {
			c.Preset = "default"
		}
	}
	for i := range spec.Audio {
		c := &spec.Audio[i]
		if c.Speed <= 0 {
			c.Speed = 1
		}
		if c.Volume < 0 {
			c.Volume = 0
		}
		if c.Volume == 0 {
			c.Volume = 100 // omitted volume means unchanged
		}
	}
}

func validateTimelineSpec(spec *TimelineSpec) error {
	if spec.Width < 16 || spec.Width > timelineMaxWidth ||
		spec.Height < 16 || spec.Height > timelineMaxHeight {
		return fmt.Errorf("unsupported canvas size %dx%d", spec.Width, spec.Height)
	}
	if spec.FPS < 1 || spec.FPS > 120 {
		return fmt.Errorf("unsupported frame rate %.2f", spec.FPS)
	}
	if spec.Duration <= 0 || spec.Duration > timelineMaxDuration {
		return fmt.Errorf("timeline duration out of range: %.2f seconds", spec.Duration)
	}
	if len(spec.Sources) == 0 {
		return fmt.Errorf("render spec has no sources")
	}
	if len(spec.Sources) > timelineMaxSources {
		return fmt.Errorf("too many sources (%d, max %d)", len(spec.Sources), timelineMaxSources)
	}
	if len(spec.Video)+len(spec.Audio) == 0 {
		return fmt.Errorf("render spec has no clips")
	}
	if len(spec.Video)+len(spec.Audio) > timelineMaxClips {
		return fmt.Errorf("too many clips (%d, max %d)", len(spec.Video)+len(spec.Audio), timelineMaxClips)
	}

	sourceType := map[string]string{}
	for _, s := range spec.Sources {
		if !timelineSourceIDPattern.MatchString(s.ID) {
			return fmt.Errorf("invalid source id %q", s.ID)
		}
		if s.Type != "video" && s.Type != "image" && s.Type != "audio" {
			return fmt.Errorf("source %s has unsupported type %q", s.ID, s.Type)
		}
		if s.Path == "" {
			return fmt.Errorf("source %s has no path", s.ID)
		}
		if _, exists := sourceType[s.ID]; exists {
			return fmt.Errorf("duplicated source id %q", s.ID)
		}
		sourceType[s.ID] = s.Type
	}

	for i, c := range spec.Video {
		t, ok := sourceType[c.Source]
		if !ok {
			return fmt.Errorf("video clip %d references unknown source %q", i, c.Source)
		}
		if t == "audio" {
			return fmt.Errorf("video clip %d references audio source %q", i, c.Source)
		}
		if err := validateClipTiming(c.Start, c.In, c.Out, c.Speed); err != nil {
			return fmt.Errorf("video clip %d: %v", i, err)
		}
		if c.Crop != "fit" && c.Crop != "fill" && c.Crop != "stretch" {
			return fmt.Errorf("video clip %d has unsupported crop mode %q", i, c.Crop)
		}
		if c.Preset != "default" && c.Preset != "warm" && c.Preset != "cool" {
			return fmt.Errorf("video clip %d has unsupported preset %q", i, c.Preset)
		}
	}

	for i, c := range spec.Audio {
		t, ok := sourceType[c.Source]
		if !ok {
			return fmt.Errorf("audio clip %d references unknown source %q", i, c.Source)
		}
		if t == "image" {
			return fmt.Errorf("audio clip %d references image source %q", i, c.Source)
		}
		if err := validateClipTiming(c.Start, c.In, c.Out, c.Speed); err != nil {
			return fmt.Errorf("audio clip %d: %v", i, err)
		}
	}
	return nil
}

func validateClipTiming(start, in, out, speed float64) error {
	if start < 0 || in < 0 {
		return fmt.Errorf("negative start or in point")
	}
	if out-in < 0.01 {
		return fmt.Errorf("in/out range too short")
	}
	if speed < 0.0625 || speed > 16 {
		return fmt.Errorf("speed %.4f out of range (0.0625 - 16)", speed)
	}
	return nil
}

/* ---------- filter graph helpers ---------- */

// ffnum formats a float for use inside an ffmpeg filter string.
func ffnum(f float64) string {
	return strconv.FormatFloat(math.Round(f*1000000)/1000000, 'f', -1, 64)
}

// visibleDuration is the clip length on the timeline after speed change.
func (c *TimelineVideoClip) visibleDuration() float64 {
	return (c.Out - c.In) / c.Speed
}

func (c *TimelineAudioClip) visibleDuration() float64 {
	return (c.Out - c.In) / c.Speed
}

// atempoChain decomposes a playback speed into atempo stages, each
// kept inside the filter's supported 0.5 - 2.0 range.
func atempoChain(speed float64) []string {
	out := []string{}
	if speed == 1 {
		return out
	}
	for speed > 2.0 {
		out = append(out, "atempo=2")
		speed /= 2.0
	}
	for speed < 0.5 {
		out = append(out, "atempo=0.5")
		speed /= 0.5
	}
	if math.Abs(speed-1) > 0.0001 {
		out = append(out, "atempo="+ffnum(speed))
	}
	return out
}

// sepiaMix returns a colorchannelmixer filter that blends the identity
// matrix with the CSS sepia matrix by amount (0 - 1).
func sepiaMix(amount float64) string {
	a := math.Min(math.Max(amount, 0), 1)
	mix := func(identity, sepia float64) string {
		return ffnum(identity*(1-a) + sepia*a)
	}
	return "colorchannelmixer=" +
		"rr=" + mix(1, 0.393) + ":rg=" + mix(0, 0.769) + ":rb=" + mix(0, 0.189) +
		":gr=" + mix(0, 0.349) + ":gg=" + mix(1, 0.686) + ":gb=" + mix(0, 0.168) +
		":br=" + mix(0, 0.272) + ":bg=" + mix(0, 0.534) + ":bb=" + mix(1, 0.131)
}

// invertMix returns a lutrgb filter implementing a partial CSS invert():
// out = a*(255-v) + (1-a)*v = a*255 + v*(1-2a)
func invertMix(amount float64) string {
	a := math.Min(math.Max(amount, 0), 1)
	expr := "'clip(" + ffnum(a*255) + "+val*" + ffnum(1-2*a) + ",0,255)'"
	return "lutrgb=r=" + expr + ":g=" + expr + ":b=" + expr
}

// effectFilters maps one effect-stack entry to ffmpeg filters. The
// canvas width scale factor keeps pixel-based effects proportional to
// the preview (which is tuned against a 1920 px wide canvas).
// Unknown types and fades (handled through the clip fade fields) are
// skipped so newer front ends stay compatible with older hosts.
func effectFilters(e TimelineEffect, canvasScale float64) []string {
	switch e.Type {
	case "bw":
		return []string{"hue=s=" + ffnum(math.Max(0, 1-e.Amount/100))}
	case "sepia":
		return []string{sepiaMix(e.Amount / 100)}
	case "invert":
		return []string{invertMix(e.Amount / 100)}
	case "hue":
		return []string{"hue=h=" + ffnum(math.Max(-180, math.Min(180, e.Amount)))}
	case "blur":
		sigma := math.Max(0, e.Amount) * canvasScale * 0.5
		if sigma <= 0 {
			return nil
		}
		return []string{"gblur=sigma=" + ffnum(sigma)}
	case "pixelate":
		block := math.Max(2, math.Min(64, e.Amount))
		b := ffnum(block)
		return []string{
			"scale=w='max(2,trunc(iw/" + b + "))':h='max(2,trunc(ih/" + b + "))'",
			"scale=w='trunc(iw*" + b + ")':h='trunc(ih*" + b + ")':flags=neighbor",
		}
	case "vignette":
		angle := math.Max(0, math.Min(1, e.Amount/100)) * math.Pi / 2
		if angle <= 0 {
			return nil
		}
		return []string{"vignette=angle=" + ffnum(angle)}
	case "grain":
		strength := math.Max(0, math.Min(100, e.Amount)) * 0.24
		if strength <= 0 {
			return nil
		}
		return []string{"noise=alls=" + ffnum(strength) + ":allf=t+u"}
	}
	return nil
}

// buildVideoClipChain emits the filter chain that turns input stream
// [inputIdx:v] into the positioned, styled overlay stream for one clip.
func buildVideoClipChain(inputIdx int, c *TimelineVideoClip, srcType string, spec *TimelineSpec, label string) string {
	visDur := c.visibleDuration()
	totDur := visDur + math.Max(0, c.Extend)
	chain := []string{}

	// Trim to the source range, reset timestamps, apply speed
	if srcType == "image" {
		// Image inputs are looped stills; simply bound the duration
		chain = append(chain, "trim=duration="+ffnum(totDur), "setpts=PTS-STARTPTS")
	} else {
		chain = append(chain, "trim=start="+ffnum(c.In)+":end="+ffnum(c.Out))
		if c.Speed != 1 {
			chain = append(chain, "setpts=(PTS-STARTPTS)/"+ffnum(c.Speed))
		} else {
			chain = append(chain, "setpts=PTS-STARTPTS")
		}
	}
	chain = append(chain, "fps="+ffnum(spec.FPS))
	if srcType != "image" && c.Extend > 0 {
		// Freeze the last frame so a transition can blend over it
		chain = append(chain, "tpad=stop_mode=clone:stop_duration="+ffnum(c.Extend))
	}
	chain = append(chain, "format=rgba")

	// Scale to the canvas per crop mode, including the user scale
	userScale := c.Scale / 100
	W := strconv.Itoa(spec.Width)
	H := strconv.Itoa(spec.Height)
	switch c.Crop {
	case "stretch":
		w := int(math.Max(2, math.Round(float64(spec.Width)*userScale)))
		h := int(math.Max(2, math.Round(float64(spec.Height)*userScale)))
		chain = append(chain, "scale=w="+strconv.Itoa(w)+":h="+strconv.Itoa(h))
	case "fill":
		f := "max(" + W + "/iw," + H + "/ih)*" + ffnum(userScale)
		chain = append(chain, "scale=w='max(2,trunc(iw*"+f+"))':h='max(2,trunc(ih*"+f+"))'")
	default: // fit
		f := "min(" + W + "/iw," + H + "/ih)*" + ffnum(userScale)
		chain = append(chain, "scale=w='max(2,trunc(iw*"+f+"))':h='max(2,trunc(ih*"+f+"))'")
	}

	// Colour pipeline: exposure (linear multiply), contrast/saturation, preset
	colorFiltersUsed := false
	if c.Exposure != 0 {
		k := ffnum(math.Max(0, 1+c.Exposure))
		chain = append(chain, "colorchannelmixer=rr="+k+":gg="+k+":bb="+k)
		colorFiltersUsed = true
	}
	eqParts := []string{}
	if c.Contrast != 0 {
		eqParts = append(eqParts, "contrast="+ffnum(1+c.Contrast/100))
	}
	if c.Saturation != 1 {
		eqParts = append(eqParts, "saturation="+ffnum(math.Min(3, c.Saturation)))
	}
	if len(eqParts) > 0 {
		chain = append(chain, "eq="+strings.Join(eqParts, ":"))
		colorFiltersUsed = true
	}
	if c.Preset == "warm" {
		chain = append(chain, sepiaMix(0.28))
		colorFiltersUsed = true
	} else if c.Preset == "cool" {
		chain = append(chain, "hue=h=-18")
		colorFiltersUsed = true
	}

	// Effect stack (fades are expressed through the fade fields instead)
	canvasScale := float64(spec.Width) / 1920
	mirror := false
	for _, e := range c.Effects {
		if e.Type == "mirror" {
			mirror = !mirror
			continue
		}
		fl := effectFilters(e, canvasScale)
		if len(fl) > 0 {
			chain = append(chain, fl...)
			colorFiltersUsed = true
		}
	}

	// YUV-based colour filters (eq, hue, vignette, noise) silently drop
	// the alpha plane; restore it so rotation fill, opacity and alpha
	// fades downstream keep working
	if colorFiltersUsed {
		chain = append(chain, "format=rgba")
	}

	// Geometry: flips then rotation (both around the clip centre)
	if c.FlipH != mirror {
		chain = append(chain, "hflip")
	}
	if c.FlipV {
		chain = append(chain, "vflip")
	}
	if c.Rotation != 0 {
		rad := ffnum(c.Rotation * math.Pi / 180)
		chain = append(chain, "rotate=a="+rad+":ow=rotw("+rad+"):oh=roth("+rad+"):c=black@0")
	}

	// Static opacity, then time-based alpha ramps
	if c.Opacity < 100 {
		chain = append(chain, "colorchannelmixer=aa="+ffnum(math.Max(0, c.Opacity)/100))
	}
	if c.FadeIn > 0 {
		chain = append(chain, "fade=t=in:st=0:d="+ffnum(c.FadeIn)+":alpha=1")
	}
	if c.TransFadeIn > 0 {
		chain = append(chain, "fade=t=in:st="+ffnum(c.TransFadeInOffset)+":d="+ffnum(c.TransFadeIn)+":alpha=1")
	}
	if c.FadeOut > 0 {
		st := math.Max(0, visDur-c.FadeOut)
		chain = append(chain, "fade=t=out:st="+ffnum(st)+":d="+ffnum(c.FadeOut)+":alpha=1")
	}
	if c.ExtendFadeOut > 0 {
		st := math.Max(0, totDur-c.ExtendFadeOut)
		chain = append(chain, "fade=t=out:st="+ffnum(st)+":d="+ffnum(c.ExtendFadeOut)+":alpha=1")
	}

	// Shift onto the timeline position
	chain = append(chain, "setpts=PTS+"+ffnum(c.Start)+"/TB")

	return "[" + strconv.Itoa(inputIdx) + ":v]" + strings.Join(chain, ",") + "[" + label + "]"
}

// buildAudioClipChain emits the filter chain for one audio contribution.
func buildAudioClipChain(inputIdx int, c *TimelineAudioClip, label string) string {
	visDur := c.visibleDuration()
	chain := []string{
		"atrim=start=" + ffnum(c.In) + ":end=" + ffnum(c.Out),
		"asetpts=PTS-STARTPTS",
	}
	chain = append(chain, atempoChain(c.Speed)...)
	if c.Volume != 100 {
		chain = append(chain, "volume="+ffnum(c.Volume/100))
	}
	if c.FadeIn > 0 {
		chain = append(chain, "afade=t=in:st=0:d="+ffnum(c.FadeIn))
	}
	if c.FadeOut > 0 {
		st := math.Max(0, visDur-c.FadeOut)
		chain = append(chain, "afade=t=out:st="+ffnum(st)+":d="+ffnum(c.FadeOut))
	}
	chain = append(chain, "aformat=sample_fmts=fltp:sample_rates=44100:channel_layouts=stereo")
	if c.Start > 0 {
		ms := int64(math.Round(c.Start * 1000))
		chain = append(chain, "adelay="+strconv.FormatInt(ms, 10)+":all=1")
	}
	return "[" + strconv.Itoa(inputIdx) + ":a]" + strings.Join(chain, ",") + "[" + label + "]"
}

// buildTimelineArgs assembles the complete ffmpeg argument list (inputs,
// filter graph, stream maps and encoder settings) for a validated spec.
// The output path and progress options are appended by the caller.
func buildTimelineArgs(spec *TimelineSpec, outputExt string) ([]string, error) {
	sourceByID := map[string]*TimelineSource{}
	for i := range spec.Sources {
		sourceByID[spec.Sources[i].ID] = &spec.Sources[i]
	}

	args := []string{}
	filters := []string{}

	// Inputs: one per clip so the same source can be used many times
	inputCount := 0
	videoInputIdx := make([]int, len(spec.Video))
	for i := range spec.Video {
		src := sourceByID[spec.Video[i].Source]
		if src.Type == "image" {
			args = append(args, "-loop", "1", "-framerate", ffnum(spec.FPS))
		}
		args = append(args, "-i", src.Path)
		videoInputIdx[i] = inputCount
		inputCount++
	}
	audioInputIdx := make([]int, len(spec.Audio))
	for i := range spec.Audio {
		args = append(args, "-i", sourceByID[spec.Audio[i].Source].Path)
		audioInputIdx[i] = inputCount
		inputCount++
	}

	// Background canvas
	filters = append(filters, "color=c=black:s="+strconv.Itoa(spec.Width)+"x"+strconv.Itoa(spec.Height)+
		":r="+ffnum(spec.FPS)+":d="+ffnum(spec.Duration)+",format=rgba[bg0]")

	// Paint every video clip onto the canvas in order
	for i := range spec.Video {
		c := &spec.Video[i]
		clipLabel := "v" + strconv.Itoa(i)
		filters = append(filters, buildVideoClipChain(videoInputIdx[i], c, sourceByID[c.Source].Type, spec, clipLabel))

		totDur := c.visibleDuration() + math.Max(0, c.Extend)
		overlay := "overlay=x='(main_w-overlay_w)/2+(" + ffnum(c.X) + ")':y='(main_h-overlay_h)/2+(" + ffnum(c.Y) + ")'" +
			":eof_action=pass:format=auto:enable='between(t," + ffnum(c.Start) + "," + ffnum(c.Start+totDur+0.04) + ")'"
		filters = append(filters, "[bg"+strconv.Itoa(i)+"]["+clipLabel+"]"+overlay+"[bg"+strconv.Itoa(i+1)+"]")
	}
	filters = append(filters, "[bg"+strconv.Itoa(len(spec.Video))+"]format=yuv420p[vout]")

	// Mix the audio contributions
	if len(spec.Audio) == 0 {
		filters = append(filters, "anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration="+ffnum(spec.Duration)+"[aout]")
	} else {
		mixInputs := ""
		for i := range spec.Audio {
			label := "a" + strconv.Itoa(i)
			filters = append(filters, buildAudioClipChain(audioInputIdx[i], &spec.Audio[i], label))
			mixInputs += "[" + label + "]"
		}
		if len(spec.Audio) == 1 {
			filters = append(filters, mixInputs+"atrim=duration="+ffnum(spec.Duration)+"[aout]")
		} else {
			filters = append(filters, mixInputs+"amix=inputs="+strconv.Itoa(len(spec.Audio))+
				":duration=longest:dropout_transition=0:normalize=0,atrim=duration="+ffnum(spec.Duration)+"[aout]")
		}
	}

	args = append(args, "-filter_complex", strings.Join(filters, ";"))
	args = append(args, "-map", "[vout]", "-map", "[aout]")

	// Encoder settings by container
	switch strings.ToLower(outputExt) {
	case ".mp4":
		crf := 21
		if spec.Quality > 0 {
			crf = 1 + spec.Quality*50/100
		}
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", strconv.Itoa(crf),
			"-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "192k",
			"-movflags", "+faststart")
	case ".webm":
		crf := 32
		if spec.Quality > 0 {
			crf = 1 + spec.Quality*62/100
		}
		args = append(args,
			"-c:v", "libvpx-vp9", "-b:v", "0", "-crf", strconv.Itoa(crf),
			"-row-mt", "1",
			"-pix_fmt", "yuv420p",
			"-c:a", "libopus", "-b:a", "160k")
	default:
		return nil, fmt.Errorf("unsupported output container %q (use .mp4 or .webm)", outputExt)
	}

	return args, nil
}

// sourceHasAudioStream reports whether ffprobe finds at least one audio
// stream in the file. When ffprobe is unavailable or fails the source
// is assumed to have audio so rendering still gets attempted.
func sourceHasAudioStream(path string) bool {
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json",
		"-show_streams", "-select_streams", "a", path)
	output, err := cmd.Output()
	if err != nil {
		return true
	}
	var result struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return true
	}
	return len(result.Streams) > 0
}

// dropSilentAudioClips removes audio clips whose source file has no
// audio stream (e.g. a video shot without sound), which would otherwise
// abort the whole ffmpeg run with a stream-matching error.
func dropSilentAudioClips(spec *TimelineSpec) {
	if len(spec.Audio) == 0 {
		return
	}
	hasAudio := map[string]bool{}
	kept := spec.Audio[:0]
	for _, c := range spec.Audio {
		for i := range spec.Sources {
			if spec.Sources[i].ID == c.Source {
				if _, probed := hasAudio[c.Source]; !probed {
					hasAudio[c.Source] = sourceHasAudioStream(spec.Sources[i].Path)
				}
				break
			}
		}
		if hasAudio[c.Source] {
			kept = append(kept, c)
		}
	}
	spec.Audio = kept
}

// FFmpeg_render_timeline renders a validated timeline spec (with local
// file paths already substituted into Sources) into the output file.
//
//   - progressFile – real filesystem path for JSON progress updates; "" disables tracking.
//
// The output container is chosen from the output file extension
// (.mp4 or .webm).
func FFmpeg_render_timeline(spec *TimelineSpec, output string, progressFile string) error {
	startTime := time.Now()

	dropSilentAudioClips(spec)

	args, err := buildTimelineArgs(spec, filepath.Ext(output))
	if err != nil {
		return err
	}
	args = append([]string{"-y"}, args...)

	var inputSize int64
	for _, s := range spec.Sources {
		inputSize += fileSize(s.Path)
	}

	var doneCh chan struct{}
	var wg *sync.WaitGroup
	ffmpegPipeFile := ""
	if progressFile != "" {
		ffmpegPipeFile = progressFile + ".ffprog"
		args = append(args, "-progress", ffmpegPipeFile)
		totalDurationMs := int64(spec.Duration * 1000)
		doneCh, wg = startProgressMonitor(ffmpegPipeFile, progressFile, output, inputSize, totalDurationMs, startTime)
		writeProgressJSON(progressFile, inputSize, output, startTime, 0.0, false)
	}

	args = append(args, output)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()

	if progressFile != "" {
		stopProgressMonitor(doneCh, wg, ffmpegPipeFile, progressFile, output, inputSize, startTime, err)
	}
	if err != nil {
		return fmt.Errorf("ffmpeg timeline render failed: %v", err)
	}
	return nil
}
