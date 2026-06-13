package facerecognition

/*
	service.go

	Deep face-embedding engine that delegates to an external HTTP service. This
	is the portable deep option: it is pure net/http (no cgo, no native library,
	no build tags) so it works on every platform — including Windows — and keeps
	the ArozOS binary self-contained. The heavy model runs in a separate service
	the administrator operates (a reference implementation ships under
	tools/face-embedding-service/).

	Service contract (see the reference service for an example):

	  GET  {baseURL}/info   -> 200 {"dimension": 512, "model": "..."}
	  POST {baseURL}/embed  (body: image/jpeg of a face crop)
	                        -> 200 {"embedding": [floats...]}
	                        or  {"error": "no face detected"}

	An optional bearer token is sent as the Authorization header. Embeddings are
	L2-normalized here and compared with cosine distance, exactly like the ONNX
	engine, so all of the clustering / people logic is shared.
*/

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nfnt/resize"
)

const (
	serviceMaxImageSide = 320              //Bound the JPEG sent to the service
	serviceJPEGQuality  = 90               //Quality of the face crop sent for embedding
	serviceHTTPTimeout  = 20 * time.Second //Per-request timeout to the service
	serviceCropMargin   = 0.6              //Generous crop so the service can detect/align the face
)

type serviceInfoResponse struct {
	Dimension int    `json:"dimension"`
	Model     string `json:"model"`
	Error     string `json:"error"`
}

type serviceEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
	Error     string    `json:"error"`
}

// serviceEngine implements faceEngine by calling an external HTTP embedding
// service.
type serviceEngine struct {
	base      string
	token     string
	dimension int
	client    *http.Client
}

// newServiceEngine validates connectivity to the embedding service (via /info)
// and records the embedding dimension it reports.
func newServiceEngine(baseURL string, token string) (faceEngine, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, errors.New("no embedding service URL configured")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, errors.New("embedding service URL must start with http:// or https://")
	}

	engine := &serviceEngine{
		base:   base,
		token:  strings.TrimSpace(token),
		client: &http.Client{Timeout: serviceHTTPTimeout},
	}

	info, err := engine.fetchInfo()
	if err != nil {
		return nil, err
	}
	if info.Dimension <= 0 {
		return nil, errors.New("embedding service reported an invalid dimension")
	}
	engine.dimension = info.Dimension
	return engine, nil
}

func (e *serviceEngine) Dimension() int { return e.dimension }

func (e *serviceEngine) Close() {}

// fetchInfo queries the service /info endpoint
func (e *serviceEngine) fetchInfo() (*serviceInfoResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), serviceHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/info", nil)
	if err != nil {
		return nil, err
	}
	e.applyAuth(req)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach embedding service: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding service /info returned HTTP %d", resp.StatusCode)
	}
	info := &serviceInfoResponse{}
	if err := json.Unmarshal(body, info); err != nil {
		return nil, fmt.Errorf("invalid /info response from embedding service: %w", err)
	}
	if info.Error != "" {
		return nil, errors.New(info.Error)
	}
	return info, nil
}

// Embed sends a face crop to the service and returns its L2-normalized embedding
func (e *serviceEngine) Embed(face image.Image) ([]float32, error) {
	jpegBytes, err := encodeFaceJPEG(face)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), serviceHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/embed", bytes.NewReader(jpegBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "image/jpeg")
	e.applyAuth(req)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding service returned HTTP %d", resp.StatusCode)
	}
	out := serviceEmbedResponse{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("invalid embed response from embedding service: %w", err)
	}
	if out.Error != "" {
		return nil, errors.New(out.Error)
	}
	if len(out.Embedding) == 0 {
		return nil, errors.New("embedding service returned an empty embedding")
	}

	result := make([]float32, len(out.Embedding))
	copy(result, out.Embedding)
	l2normalize(result)
	return result, nil
}

func (e *serviceEngine) applyAuth(req *http.Request) {
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
}

// encodeFaceJPEG resizes a face crop (if large) and JPEG-encodes it for sending
// to the embedding service.
func encodeFaceJPEG(face image.Image) ([]byte, error) {
	b := face.Bounds()
	longest := b.Dx()
	if b.Dy() > longest {
		longest = b.Dy()
	}
	img := face
	if longest > serviceMaxImageSide {
		if b.Dx() >= b.Dy() {
			img = resize.Resize(serviceMaxImageSide, 0, face, resize.Bilinear)
		} else {
			img = resize.Resize(0, serviceMaxImageSide, face, resize.Bilinear)
		}
	}
	buf := bytes.Buffer{}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: serviceJPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
