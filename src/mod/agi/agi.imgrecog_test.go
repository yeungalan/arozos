package agi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/robertkrimen/otto"
	"imuslab.com/arozos/mod/agi/static"
)

// imgRecogGateway returns a DB-backed Gateway with the image recognition table
// ready for use.
func imgRecogGateway(t *testing.T) *Gateway {
	t.Helper()
	g := dbGateway(t)
	g.Option.UserHandler.GetDatabase().NewTable(imageRecognitionDBTable)
	return g
}

func TestImageRecognitionConfigPersistence(t *testing.T) {
	g := imgRecogGateway(t)

	//Defaults: empty + disabled.
	if cfg := g.getImageRecognitionConfig(); cfg.Enabled || cfg.Endpoint != "" {
		t.Errorf("expected empty default config, got %+v", cfg)
	}

	want := ImageRecognitionConfig{Endpoint: "http://localhost:12810", Enabled: true, Manual: true}
	if err := g.SaveImageRecognitionConfig(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := g.getImageRecognitionConfig()
	if got != want {
		t.Errorf("roundtrip = %+v, want %+v", got, want)
	}
}

func TestApplyDetectedEndpoint(t *testing.T) {
	t.Run("installed enables and sets endpoint", func(t *testing.T) {
		g := imgRecogGateway(t)
		g.ApplyDetectedImageRecognitionEndpoint("http://localhost:12810", true)
		cfg := g.getImageRecognitionConfig()
		if !cfg.Enabled || cfg.Endpoint != "http://localhost:12810" {
			t.Errorf("installed: got %+v", cfg)
		}
	})

	t.Run("not installed disables", func(t *testing.T) {
		g := imgRecogGateway(t)
		g.SaveImageRecognitionConfig(ImageRecognitionConfig{Endpoint: "http://localhost:12810", Enabled: true})
		g.ApplyDetectedImageRecognitionEndpoint("", false)
		if g.getImageRecognitionConfig().Enabled {
			t.Error("expected disabled when subservice absent")
		}
	})

	t.Run("manual config is respected", func(t *testing.T) {
		g := imgRecogGateway(t)
		manual := ImageRecognitionConfig{Endpoint: "http://remote:9000", Enabled: true, Manual: true}
		g.SaveImageRecognitionConfig(manual)
		g.ApplyDetectedImageRecognitionEndpoint("http://localhost:12810", true)
		if got := g.getImageRecognitionConfig(); got != manual {
			t.Errorf("manual override changed: %+v", got)
		}
	})
}

func TestExtractServiceError(t *testing.T) {
	if msg := extractServiceError([]byte(`{"error":true,"message":"bad image"}`)); msg != "bad image" {
		t.Errorf("got %q, want 'bad image'", msg)
	}
	if msg := extractServiceError([]byte(`not json`)); msg != "" {
		t.Errorf("expected empty for non-json, got %q", msg)
	}
}

func TestImageRecognitionGetAgainstMock(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/face/people" {
			w.Write([]byte(`{"count":1,"people":[{"uuid":"abc"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	g := imgRecogGateway(t)
	g.SaveImageRecognitionConfig(ImageRecognitionConfig{Endpoint: mock.URL, Enabled: true})

	body, err := g.imageRecognitionGet("/api/face/people")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(string(body), `"uuid":"abc"`) {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestImageRecognitionGetDisabled(t *testing.T) {
	g := imgRecogGateway(t)
	if _, err := g.imageRecognitionGet("/api/face/people"); err == nil {
		t.Error("expected error when disabled")
	}
}

func TestImageRecognitionDoSurfacesServiceError(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":true,"message":"corrupt image"}`))
	}))
	defer mock.Close()

	req, _ := http.NewRequest("GET", mock.URL+"/api/tag", nil)
	_, err := imageRecognitionDo(req)
	if err == nil || !strings.Contains(err.Error(), "corrupt image") {
		t.Errorf("expected service error to surface, got %v", err)
	}
}

func TestHandleImageRecognitionConfig(t *testing.T) {
	g := imgRecogGateway(t)

	//POST a manual config.
	form := url.Values{"endpoint": {"http://localhost:12810"}, "enabled": {"true"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/system/imagerecognition/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	g.HandleImageRecognitionConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d", rec.Code)
	}
	cfg := g.getImageRecognitionConfig()
	if !cfg.Enabled || !cfg.Manual || cfg.Endpoint != "http://localhost:12810" {
		t.Errorf("config after POST = %+v", cfg)
	}

	//GET returns the stored config as JSON.
	rec = httptest.NewRecorder()
	g.HandleImageRecognitionConfig(rec, httptest.NewRequest(http.MethodGet, "/system/imagerecognition/config", nil))
	var got ImageRecognitionConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET body not JSON: %v (%s)", err, rec.Body.String())
	}
	if got.Endpoint != "http://localhost:12810" {
		t.Errorf("GET config = %+v", got)
	}

	//POST auto=true clears the manual flag so auto-detection resumes.
	rec = httptest.NewRecorder()
	areq := httptest.NewRequest(http.MethodPost, "/system/imagerecognition/config", strings.NewReader("auto=true"))
	areq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	g.HandleImageRecognitionConfig(rec, areq)
	if g.getImageRecognitionConfig().Manual {
		t.Error("auto=true should clear manual flag")
	}
}

func TestHandleImageRecognitionTest(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/info" {
			w.Write([]byte(`{"name":"Image Recognition","backend":"builtin-scene"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	g := imgRecogGateway(t)
	rec := httptest.NewRecorder()
	form := url.Values{"endpoint": {mock.URL}}
	req := httptest.NewRequest(http.MethodPost, "/system/imagerecognition/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	g.HandleImageRecognitionTest(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "builtin-scene") {
		t.Errorf("test handler: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestImageRecognitionVMRoundTrip drives the JavaScript-facing API through the
// Otto VM against a mock subservice, exercising the same path an AGI script
// uses for the calls that do not need a virtual file.
func TestImageRecognitionVMRoundTrip(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/face/people" {
			w.Write([]byte(`{"count":2,"people":[{"uuid":"p1"},{"uuid":"p2"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mock.Close()

	g := imgRecogGateway(t)
	g.SaveImageRecognitionConfig(ImageRecognitionConfig{Endpoint: mock.URL, Enabled: true})

	vm := otto.New()
	g.injectImageRecognitionFunctions(&static.AgiLibInjectionPayload{VM: vm, User: nil})

	//ready() reflects the enabled config.
	ready, err := vm.Run(`imagerecognition.ready()`)
	if err != nil {
		t.Fatalf("ready(): %v", err)
	}
	if b, _ := ready.ToBoolean(); !b {
		t.Error("expected ready() to be true")
	}

	//listPeople() round-trips JSON from the mock service.
	out, err := vm.Run(`(function(){ var p = imagerecognition.listPeople(); return p.length + ":" + p[0].uuid; })()`)
	if err != nil {
		t.Fatalf("listPeople(): %v", err)
	}
	if s, _ := out.ToString(); s != "2:p1" {
		t.Errorf("listPeople round-trip = %q, want '2:p1'", s)
	}
}
