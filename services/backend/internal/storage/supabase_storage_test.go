package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ---------- BuildPath ----------

func TestBuildPath_ContentTypeMappings(t *testing.T) {
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	meetingID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	cases := map[string]string{
		"audio/webm":                  "webm",
		"audio/webm;codecs=opus":      "webm",
		"audio/ogg":                   "ogg",
		"audio/ogg; codecs=opus":      "ogg",
		"audio/mp4":                   "m4a",
		"audio/m4a":                   "m4a",
		"audio/mpeg":                  "mp3",
		"audio/mp3":                   "mp3",
		"something/unknown":           "bin",
	}
	for ct, ext := range cases {
		got := BuildPath(userID, meetingID, ct)
		want := fmt.Sprintf("%s/%s/audio.%s", userID, meetingID, ext)
		if got != want {
			t.Errorf("BuildPath(%q) = %q, want %q", ct, got, want)
		}
	}
}

// ---------- New ----------

func TestNew_MissingConfig(t *testing.T) {
	if _, err := New("", "key", "bucket"); err == nil {
		t.Error("expected error for missing storageURL")
	}
	if _, err := New("https://x", "", "bucket"); err == nil {
		t.Error("expected error for missing serviceRoleKey")
	}
}

func TestNew_DefaultBucket(t *testing.T) {
	c, err := New("https://x/storage/v1", "key", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Bucket() != "meeting-audio" {
		t.Errorf("expected default bucket 'meeting-audio', got %q", c.Bucket())
	}
}

// ---------- HTTP behaviors (httptest) ----------

func newFakeClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/storage/v1", "test-key", "meeting-audio")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUpload_SendsAuthAndContentType(t *testing.T) {
	called := false
	c := newFakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("apikey"); got != "test-key" {
			t.Errorf("apikey = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "audio/webm" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("x-upsert"); got != "true" {
			t.Errorf("x-upsert = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, []byte("dummy-audio-bytes")) {
			t.Errorf("body mismatch: %q", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"Key":"meeting-audio/abc"}`))
	})

	err := c.Upload(context.Background(), "abc/audio.webm", strings.NewReader("dummy-audio-bytes"), "audio/webm")
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}

func TestUpload_EmptyPath(t *testing.T) {
	c, _ := New("https://x/storage/v1", "k", "b")
	err := c.Upload(context.Background(), "", strings.NewReader("x"), "audio/webm")
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestUpload_EmptyContentType(t *testing.T) {
	c, _ := New("https://x/storage/v1", "k", "b")
	err := c.Upload(context.Background(), "x", strings.NewReader("x"), "")
	if err == nil {
		t.Error("expected error for empty content type")
	}
}

func TestUpload_SurfacesServerError(t *testing.T) {
	c := newFakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"no permission"}`))
	})
	err := c.Upload(context.Background(), "abc/audio.webm", strings.NewReader("x"), "audio/webm")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "no permission") {
		t.Errorf("error should include status + body, got %v", err)
	}
}

func TestDownload_ReturnsBody(t *testing.T) {
	c := newFakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		_, _ = w.Write([]byte("audio-payload"))
	})
	rc, err := c.Download(context.Background(), "abc/audio.webm")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "audio-payload" {
		t.Errorf("body = %q, want audio-payload", got)
	}
}

func TestDelete_TreatsNotFoundAsSuccess(t *testing.T) {
	c := newFakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if err := c.Delete(context.Background(), "abc"); err != nil {
		t.Errorf("404 should be swallowed, got: %v", err)
	}
}

func TestDelete_SurfacesServerError(t *testing.T) {
	c := newFakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	err := c.Delete(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status, got %v", err)
	}
}

func TestStat_ParsesSize(t *testing.T) {
	c := newFakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/object/info/") {
			t.Errorf("Stat should hit /object/info/, got path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"size":12345,"otherField":"ignored"}`))
	})
	size, err := c.Stat(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if size != 12345 {
		t.Errorf("size = %d, want 12345", size)
	}
}

func TestCreateSignedURL_BuildsAbsoluteURL(t *testing.T) {
	var capturedBody []byte
	c := newFakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/object/sign/") {
			t.Errorf("should hit /object/sign/, got %s", r.URL.Path)
		}
		capturedBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"signedURL":"/object/sign/meeting-audio/abc/audio.webm?token=xyz"}`))
	})
	u, err := c.CreateSignedURL(context.Background(), "abc/audio.webm", 900)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(capturedBody), `"expiresIn":900`) {
		t.Errorf("request body should contain expiresIn, got %s", capturedBody)
	}
	if !strings.Contains(u.URL, "/storage/v1/object/sign/meeting-audio/abc/audio.webm") {
		t.Errorf("URL should be absolute + include signed path, got %q", u.URL)
	}
	if !strings.Contains(u.URL, "token=xyz") {
		t.Errorf("URL should preserve query token, got %q", u.URL)
	}
	if u.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should be set")
	}
}

func TestCreateSignedURL_InvalidExpiry(t *testing.T) {
	c, _ := New("https://x/storage/v1", "k", "b")
	if _, err := c.CreateSignedURL(context.Background(), "x", 0); err == nil {
		t.Error("expected error for expiresIn <= 0")
	}
}

func TestList_ParsesNamesAndSizes(t *testing.T) {
	var capturedBody []byte
	c := newFakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/object/list/meeting-audio") {
			t.Errorf("should hit /object/list/<bucket>, got %s", r.URL.Path)
		}
		capturedBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`[
			{"name":"00000000","metadata":{"size":512}},
			{"name":"00000001","metadata":{"size":1024}},
			{"name":"00000002","metadata":null}
		]`))
	})
	objs, err := c.List(context.Background(), "_staging/abc/", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(capturedBody), `"prefix":"_staging/abc/"`) {
		t.Errorf("body should include prefix, got %s", capturedBody)
	}
	if !strings.Contains(string(capturedBody), `"limit":100`) {
		t.Errorf("body should include limit, got %s", capturedBody)
	}
	if len(objs) != 3 {
		t.Fatalf("len = %d, want 3", len(objs))
	}
	if objs[0].Name != "00000000" || objs[0].Size != 512 {
		t.Errorf("first item = %+v", objs[0])
	}
	if objs[2].Size != 0 {
		t.Errorf("metadata:null should yield size=0, got %d", objs[2].Size)
	}
}

func TestList_DefaultLimitApplied(t *testing.T) {
	var captured []byte
	c := newFakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := c.List(context.Background(), "_staging/abc/", 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(captured), `"limit":100`) {
		t.Errorf("default limit 100 should be applied, got body %s", captured)
	}
}
