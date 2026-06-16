package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	r := newRecognizerForTest(t)
	srv := newServer(r, ServiceInfo{Name: "Image Recognition"}, "test", newSvcLogger("[test]"))
	mux := http.NewServeMux()
	srv.routes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func multipartImage(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("image", "upload.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return &body, w.FormDataContentType()
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestHTTPTagMultipart(t *testing.T) {
	ts := newTestServer(t)
	body, ct := multipartImage(t, fixtureBytes(t, "person_a.jpg"))

	resp, err := http.Post(ts.URL+"/api/tag", ct, body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decodeJSON(t, resp)
	tags, ok := out["tags"].([]interface{})
	if !ok || len(tags) == 0 {
		t.Errorf("expected non-empty tags, got %v", out["tags"])
	}
}

func TestHTTPRecognizeMultipartAssignsUUID(t *testing.T) {
	ts := newTestServer(t)
	body, ct := multipartImage(t, fixtureBytes(t, "person_a.jpg"))

	resp, err := http.Post(ts.URL+"/api/face/recognize", ct, body)
	if err != nil {
		t.Fatal(err)
	}
	out := decodeJSON(t, resp)
	faces, ok := out["faces"].([]interface{})
	if !ok || len(faces) != 1 {
		t.Fatalf("faces = %v, want 1", out["faces"])
	}
	face := faces[0].(map[string]interface{})
	if uuid, _ := face["personUUID"].(string); uuid == "" {
		t.Errorf("recognised face missing personUUID: %v", face)
	}
}

func TestHTTPRecognizeBase64(t *testing.T) {
	ts := newTestServer(t)
	b64 := base64.StdEncoding.EncodeToString(fixtureBytes(t, "person_b.jpg"))

	resp, err := http.PostForm(ts.URL+"/api/face/recognize", map[string][]string{"image_b64": {b64}})
	if err != nil {
		t.Fatal(err)
	}
	out := decodeJSON(t, resp)
	if faces, _ := out["faces"].([]interface{}); len(faces) != 1 {
		t.Errorf("base64 path: faces = %v, want 1", out["faces"])
	}
}

func TestHTTPAnalyzeRawBody(t *testing.T) {
	ts := newTestServer(t)
	data := fixtureBytes(t, "person_c.jpg")

	resp, err := http.Post(ts.URL+"/api/analyze", "image/jpeg", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out := decodeJSON(t, resp)
	if _, ok := out["faces"].([]interface{}); !ok {
		t.Errorf("analyze raw body: missing faces in %v", out)
	}
	if _, ok := out["tags"].([]interface{}); !ok {
		t.Errorf("analyze raw body: missing tags in %v", out)
	}
}

func TestHTTPPeopleAndReset(t *testing.T) {
	ts := newTestServer(t)

	//Recognise someone so the gallery is non-empty.
	body, ct := multipartImage(t, fixtureBytes(t, "person_a.jpg"))
	if _, err := http.Post(ts.URL+"/api/face/recognize", ct, body); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(ts.URL + "/api/face/people")
	if err != nil {
		t.Fatal(err)
	}
	out := decodeJSON(t, resp)
	if count, _ := out["count"].(float64); count < 1 {
		t.Errorf("people count = %v, want >= 1", out["count"])
	}

	//Reset clears the gallery.
	resp, err = http.Post(ts.URL+"/api/face/reset", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if decodeJSON(t, resp)["success"] != true {
		t.Error("reset did not report success")
	}

	resp, _ = http.Get(ts.URL + "/api/face/people")
	if count, _ := decodeJSON(t, resp)["count"].(float64); count != 0 {
		t.Errorf("people count after reset = %v, want 0", count)
	}
}

func TestHTTPBadRequests(t *testing.T) {
	ts := newTestServer(t)

	//Empty body.
	resp, err := http.Post(ts.URL+"/api/tag", "image/jpeg", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty body status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	//Non-image bytes.
	resp, err = http.Post(ts.URL+"/api/analyze", "image/jpeg", bytes.NewReader([]byte("garbage")))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("garbage status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHTTPInfoAndHealth(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if decodeJSON(t, resp)["status"] != "ok" {
		t.Error("healthz not ok")
	}

	resp, err = http.Get(ts.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	info := decodeJSON(t, resp)
	if info["backend"] == nil || info["capabilities"] == nil {
		t.Errorf("info missing fields: %v", info)
	}
}
