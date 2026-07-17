package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ValidationResult is the outcome of a pre-save RTSP probe (spec section 2.2,
// "Stream Validation"). Fields left empty could not be determined without a
// full media decode, which is the streaming engine's job.
type ValidationResult struct {
	OK         bool   `json:"ok"`
	Reachable  bool   `json:"reachable"`
	AuthOK     bool   `json:"authOk"`
	Codec      string `json:"codec"`
	Resolution string `json:"resolution"`
	FPS        int    `json:"fps"`
	Error      string `json:"error"`  // machine-ish reason: unreachable | timeout | auth_failed | invalid_url | unsupported | protocol_error
	Detail     string `json:"detail"` // human-readable detail
}

// validateStream performs a best-effort RTSP handshake using only the standard
// net package (no ffmpeg / ffprobe — keeping the binary portable). It dials the
// server, issues OPTIONS then DESCRIBE, distinguishes an authentication failure
// (401) from an unreachable host or timeout, and parses the returned SDP to
// detect the codec (and resolution when advertised).
func validateStream(rawURL, username, password string, timeout time.Duration) ValidationResult {
	res := ValidationResult{}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (!strings.EqualFold(u.Scheme, "rtsp") && !strings.EqualFold(u.Scheme, "rtsps")) {
		res.Error = "invalid_url"
		res.Detail = "Invalid RTSP URL"
		return res
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":554"
	}

	// Credentials from the URL take precedence, else the separate fields.
	if u.User != nil {
		username = u.User.Username()
		if p, ok := u.User.Password(); ok {
			password = p
		}
	}

	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			res.Error = "timeout"
			res.Detail = "Connection to the camera timed out"
		} else {
			res.Error = "unreachable"
			res.Detail = "Camera unreachable: " + err.Error()
		}
		return res
	}
	defer conn.Close()
	res.Reachable = true

	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)
	reader := bufio.NewReader(conn)

	// RTSP request target should not include userinfo.
	target := *u
	target.User = nil
	requestURI := target.String()

	// --- OPTIONS -----------------------------------------------------------
	status, _, _, err := rtspExchange(conn, reader, "OPTIONS", requestURI, 1, "", "")
	if err != nil {
		res.Error = "protocol_error"
		res.Detail = "Reached the host but it did not speak RTSP: " + err.Error()
		return res
	}
	if status == 401 {
		// Some servers demand auth even for OPTIONS; retry with Basic below.
		if username == "" {
			res.Error = "auth_failed"
			res.Detail = "Camera requires authentication but no credentials were provided"
			return res
		}
	}

	// --- DESCRIBE (fetch SDP) ---------------------------------------------
	auth := ""
	if username != "" {
		auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	}
	status, hdrs, body, err := rtspExchange(conn, reader, "DESCRIBE", requestURI, 2, "Accept: application/sdp", auth)
	if err != nil {
		res.Error = "protocol_error"
		res.Detail = "RTSP DESCRIBE failed: " + err.Error()
		return res
	}
	switch {
	case status == 401 || status == 403:
		if strings.Contains(strings.ToLower(hdrs["www-authenticate"]), "digest") && username != "" {
			// Digest handshakes need MD5 nonce math beyond this foundation probe.
			res.AuthOK = false
			res.Error = "auth_failed"
			res.Detail = "Camera uses Digest authentication (not verified by this probe); credentials may still be valid"
			return res
		}
		res.Error = "auth_failed"
		res.Detail = "Authentication failed"
		return res
	case status >= 200 && status < 300:
		res.AuthOK = true
	default:
		res.Error = "protocol_error"
		res.Detail = fmt.Sprintf("Camera responded with RTSP status %d", status)
		return res
	}

	res.Codec, res.Resolution, res.FPS = parseSDP(body)
	if res.Codec != "" && !validCodecs[res.Codec] {
		res.Error = "unsupported"
		res.Detail = "Detected codec " + res.Codec + " is not supported"
		res.OK = false
		return res
	}
	res.OK = true
	return res
}

// rtspExchange writes one RTSP request and reads the response, returning the
// status code, lower-cased headers, the body, and any transport error.
func rtspExchange(conn net.Conn, reader *bufio.Reader, method, uri string, cseq int, extraHeader, auth string) (int, map[string]string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s RTSP/1.0\r\n", method, uri)
	fmt.Fprintf(&b, "CSeq: %d\r\n", cseq)
	fmt.Fprintf(&b, "User-Agent: ArozOS-Surveillance/%s\r\n", version)
	if extraHeader != "" {
		b.WriteString(extraHeader + "\r\n")
	}
	if auth != "" {
		b.WriteString("Authorization: " + auth + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return 0, nil, "", err
	}

	// Status line, e.g. "RTSP/1.0 200 OK".
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return 0, nil, "", err
	}
	parts := strings.Fields(statusLine)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "RTSP/") {
		return 0, nil, "", fmt.Errorf("unexpected response %q", strings.TrimSpace(statusLine))
	}
	code := 0
	fmt.Sscanf(parts[1], "%d", &code)

	headers := map[string]string{}
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, nil, "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if i := strings.Index(line, ":"); i > 0 {
			key := strings.ToLower(strings.TrimSpace(line[:i]))
			val := strings.TrimSpace(line[i+1:])
			headers[key] = val
			if key == "content-length" {
				fmt.Sscanf(val, "%d", &contentLength)
			}
		}
	}

	body := ""
	if contentLength > 0 {
		buf := make([]byte, contentLength)
		read := 0
		for read < contentLength {
			n, err := reader.Read(buf[read:])
			if n > 0 {
				read += n
			}
			if err != nil {
				break
			}
		}
		body = string(buf[:read])
	}
	return code, headers, body, nil
}

// parseSDP extracts a best-effort codec name, resolution and framerate from an
// SDP payload's rtpmap / fmtp / framerate attributes.
func parseSDP(sdp string) (codec, resolution string, fps int) {
	for _, raw := range strings.Split(sdp, "\n") {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "a=rtpmap:"):
			// a=rtpmap:96 H264/90000
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				enc := fields[1]
				if i := strings.Index(enc, "/"); i > 0 {
					enc = enc[:i]
				}
				if c := normaliseCodecName(enc); c != "" && codec == "" {
					codec = c
				}
			}
		case strings.HasPrefix(lower, "a=framerate:") || strings.HasPrefix(lower, "a=x-framerate:"):
			val := line[strings.Index(line, ":")+1:]
			f := 0
			fmt.Sscanf(strings.TrimSpace(val), "%d", &f)
			if f > 0 {
				fps = f
			}
		case strings.Contains(lower, "a=x-dimensions:"):
			// a=x-dimensions:1920,1080
			val := line[strings.Index(line, ":")+1:]
			val = strings.ReplaceAll(strings.TrimSpace(val), ",", "x")
			resolution = val
		}
	}
	return codec, resolution, fps
}

// normaliseCodecName maps SDP encoding names to the store's canonical codec
// keys.
func normaliseCodecName(enc string) string {
	switch strings.ToUpper(enc) {
	case "H264":
		return "h264"
	case "H265", "HEVC":
		return "h265"
	case "JPEG", "MJPEG":
		return "mjpeg"
	case "AV1", "AV1X":
		return "av1"
	case "MP4V-ES", "MPEG4-GENERIC":
		return "mpeg4"
	}
	return ""
}
