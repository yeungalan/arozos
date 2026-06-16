package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetUserInfoFromRequest(t *testing.T) {
	a := &ArozHandler{}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("aouser", "alice")
	r.Header.Set("aotoken", "tok-123")

	user, token := a.GetUserInfoFromRequest(nil, r)
	if user != "alice" || token != "tok-123" {
		t.Errorf("got (%q,%q), want (alice,tok-123)", user, token)
	}
}

func TestRequestGatewayInterface(t *testing.T) {
	//Stand in for the ArozOS AGI gateway: echo back the posted script.
	var gotToken, gotScript string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotToken = r.FormValue("token")
		gotScript = r.FormValue("script")
		w.Write([]byte("ok"))
	}))
	defer gateway.Close()

	a := &ArozHandler{restfulEndpoint: gateway.URL}
	resp, err := a.RequestGatewayInterface("tok-abc", `sendResp("hi")`)
	if err != nil {
		t.Fatalf("RequestGatewayInterface: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if gotToken != "tok-abc" {
		t.Errorf("gateway saw token %q, want tok-abc", gotToken)
	}
	if gotScript != `sendResp("hi")` {
		t.Errorf("gateway saw script %q", gotScript)
	}
	if string(body) != "ok" {
		t.Errorf("response body = %q, want ok", string(body))
	}
}
