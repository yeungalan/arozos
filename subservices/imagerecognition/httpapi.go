package main

import (
	"encoding/base64"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"strings"
)

/*
	httpapi.go

	The HTTP surface of the subservice. Every recognition endpoint accepts an
	image supplied as either:

	  - a multipart/form-data file field named "image", or
	  - a base64 string in the "image_b64" form field, or
	  - the raw request body (when Content-Type is image/...).

	Responses are JSON. These endpoints are reached by ArozOS through the
	authenticated subservice reverse proxy, and ultimately by AGI scripts via
	the "imagerecognition" library.
*/

const maxUploadBytes = 32 << 20 //32 MiB cap on uploaded images

// Server holds the recognizer and serves the HTTP API.
type Server struct {
	recognizer *Recognizer
	info       ServiceInfo
	version    string
	lg         *svcLogger
}

func newServer(recognizer *Recognizer, info ServiceInfo, version string, lg *svcLogger) *Server {
	return &Server{recognizer: recognizer, info: info, version: version, lg: lg}
}

// routes registers all handlers onto mux.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/info", s.handleInfo)
	mux.HandleFunc("/api/tag", s.handleTag)
	mux.HandleFunc("/api/face/detect", s.handleDetect)
	mux.HandleFunc("/api/face/recognize", s.handleRecognize)
	mux.HandleFunc("/api/face/people", s.handlePeople)
	mux.HandleFunc("/api/face/reset", s.handleReset)
	mux.HandleFunc("/api/analyze", s.handleAnalyze)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":         s.info.Name,
		"version":      s.version,
		"backend":      s.recognizer.backendName(),
		"faceDetector": s.recognizer.faceDetectorName(),
		"faceEmbedder": s.recognizer.faceEmbedderName(),
		"knownPeople":  s.recognizer.people.Count(),
		"capabilities": []string{"tagging", "face-detection", "face-recognition"},
		"mlStatus":     s.recognizer.mlStatus(),
	})
}

func (s *Server) handleTag(w http.ResponseWriter, r *http.Request) {
	img, err := s.readImage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"tags": s.recognizer.Tag(img)})
}

func (s *Server) handleDetect(w http.ResponseWriter, r *http.Request) {
	img, err := s.readImage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	faces := s.recognizer.DetectFaces(img)
	writeJSON(w, http.StatusOK, map[string]interface{}{"count": len(faces), "faces": faces})
}

func (s *Server) handleRecognize(w http.ResponseWriter, r *http.Request) {
	img, err := s.readImage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	faces := s.recognizer.RecognizeFaces(img)
	writeJSON(w, http.StatusOK, map[string]interface{}{"count": len(faces), "faces": faces})
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	img, err := s.readImage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.recognizer.Analyze(img))
}

func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	people := s.recognizer.people.People()
	writeJSON(w, http.StatusOK, map[string]interface{}{"count": len(people), "people": people})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST to reset the people gallery")
		return
	}
	if err := s.recognizer.people.Reset(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// readImage extracts a decoded image from the request using whichever of the
// supported transports the caller used.
func (s *Server) readImage(r *http.Request) (image.Image, error) {
	contentType := r.Header.Get("Content-Type")

	//Multipart upload (file field "image") or base64 form field "image_b64".
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			return nil, errBadImage("could not parse multipart form: " + err.Error())
		}
		if file, _, err := r.FormFile("image"); err == nil {
			defer file.Close()
			data, rerr := io.ReadAll(io.LimitReader(file, maxUploadBytes))
			if rerr != nil {
				return nil, errBadImage("could not read uploaded file")
			}
			return decodeOrErr(data)
		}
		if b64 := r.FormValue("image_b64"); b64 != "" {
			return decodeBase64Image(b64)
		}
		return nil, errBadImage("multipart form has no \"image\" file or \"image_b64\" field")
	}

	//URL-encoded base64 field.
	if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			return nil, errBadImage("could not parse form")
		}
		if b64 := r.FormValue("image_b64"); b64 != "" {
			return decodeBase64Image(b64)
		}
		return nil, errBadImage("form has no \"image_b64\" field")
	}

	//Raw image body.
	data, err := io.ReadAll(io.LimitReader(r.Body, maxUploadBytes))
	if err != nil {
		return nil, errBadImage("could not read request body")
	}
	if len(data) == 0 {
		return nil, errBadImage("empty request: supply an image as multipart \"image\", \"image_b64\" or a raw image body")
	}
	return decodeOrErr(data)
}

func decodeBase64Image(b64 string) (image.Image, error) {
	//Tolerate data URLs ("data:image/png;base64,....").
	if idx := strings.Index(b64, ","); strings.HasPrefix(b64, "data:") && idx >= 0 {
		b64 = b64[idx+1:]
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, errBadImage("invalid base64 image data")
	}
	return decodeOrErr(data)
}

func decodeOrErr(data []byte) (image.Image, error) {
	img, err := decodeImage(data)
	if err != nil {
		return nil, errBadImage("unsupported or corrupt image data: " + err.Error())
	}
	return img, nil
}

type badImageError struct{ msg string }

func (e badImageError) Error() string { return e.msg }
func errBadImage(msg string) error    { return badImageError{msg: msg} }

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{"error": true, "message": msg})
}
