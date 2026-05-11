package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return NewClient(s.URL, "test-token", 2*time.Second), s
}

func decodeBody(t *testing.T, r *http.Request, out any) {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal body %q: %v", string(b), err)
	}
}

func TestClient_AuthHeader(t *testing.T) {
	var got string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.AppInfo(context.Background()); err != nil {
		t.Fatalf("AppInfo: %v", err)
	}
	if got != "test-token" {
		t.Errorf("Authorization header = %q, want %q", got, "test-token")
	}
}

func TestClient_BaseURL_TrailingSlashTrimmed(t *testing.T) {
	c := NewClient("http://example.com/", "tok", 0)
	urls := c.URLs()
	if len(urls) != 1 || urls[0] != "http://example.com" {
		t.Errorf("URLs() = %v, want [http://example.com]", urls)
	}
}

func TestClient_FallbackToSecondURL_OnTransportError(t *testing.T) {
	// First URL: a port nothing is listening on -> connection refused.
	// Second URL: a real test server that succeeds.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"appVersion":"x"}`))
	}))
	t.Cleanup(good.Close)
	c := NewClient("http://127.0.0.1:1/,"+good.URL, "tok", 2*time.Second)
	if _, err := c.AppInfo(context.Background()); err != nil {
		t.Fatalf("AppInfo should have fallen back and succeeded: %v", err)
	}
}

func TestClient_DoesNotFallback_OnHTTPError(t *testing.T) {
	// Both URLs respond, but the first one with 404. Client should NOT fall back —
	// a 404 from the server is a real answer, not a transport failure.
	firstHits, secondHits := 0, 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
	}))
	t.Cleanup(first.Close)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(second.Close)
	c := NewClient(first.URL+","+second.URL, "tok", 2*time.Second)
	if _, err := c.AppInfo(context.Background()); err == nil {
		t.Fatal("expected error from first URL")
	}
	if firstHits != 1 || secondHits != 0 {
		t.Errorf("first=%d second=%d, want 1,0 (no fallback on HTTP 404)", firstHits, secondHits)
	}
}

func TestClient_AllURLsDown_ReturnsLastError(t *testing.T) {
	c := NewClient("http://127.0.0.1:1/,http://127.0.0.1:2/", "tok", 2*time.Second)
	if _, err := c.AppInfo(context.Background()); err == nil {
		t.Fatal("expected error when all URLs unreachable")
	}
}

func TestClient_CreateNote_DefaultsAndPath(t *testing.T) {
	var (
		path string
		body CreateNoteRequest
	)
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		decodeBody(t, r, &body)
		_, _ = w.Write([]byte(`{"note":{"noteId":"NEW","title":"x","type":"text"},"branch":{"branchId":"root_NEW","noteId":"NEW","parentNoteId":"root"}}`))
	})

	resp, err := c.CreateNote(context.Background(), CreateNoteRequest{Title: "x"})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if path != "/etapi/create-note" {
		t.Errorf("path = %q, want /etapi/create-note", path)
	}
	if body.Type != "text" {
		t.Errorf("default Type = %q, want 'text'", body.Type)
	}
	if body.ParentNoteID != "root" {
		t.Errorf("default ParentNoteID = %q, want 'root'", body.ParentNoteID)
	}
	if resp.Note.NoteID != "NEW" {
		t.Errorf("note id = %q, want NEW", resp.Note.NoteID)
	}
	if resp.Branch.ParentNoteID != "root" {
		t.Errorf("branch parent = %q, want root", resp.Branch.ParentNoteID)
	}
}

func TestClient_SearchNotes_QueryEncoding(t *testing.T) {
	var rawQuery url.Values
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"results":[]}`))
	})

	_, err := c.SearchNotes(context.Background(), SearchOpts{
		Query:           "#status=active",
		AncestorNoteID:  "ABC",
		FastSearch:      true,
		IncludeArchived: true,
		Limit:           7,
	})
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	checks := map[string]string{
		"search":               "#status=active",
		"ancestorNoteId":       "ABC",
		"fastSearch":           "true",
		"includeArchivedNotes": "true",
		"limit":                "7",
	}
	for k, want := range checks {
		if got := rawQuery.Get(k); got != want {
			t.Errorf("query[%s] = %q, want %q", k, got, want)
		}
	}
}

func TestClient_CreateAttribute_DefaultType(t *testing.T) {
	var body Attribute
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		decodeBody(t, r, &body)
		_, _ = w.Write([]byte(`{"attributeId":"A","noteId":"N","type":"label","name":"x","value":"y"}`))
	})
	if _, err := c.CreateAttribute(context.Background(), Attribute{NoteID: "N", Name: "x", Value: "y"}); err != nil {
		t.Fatalf("CreateAttribute: %v", err)
	}
	if body.Type != "label" {
		t.Errorf("default Type = %q, want 'label'", body.Type)
	}
}

func TestClient_UpdateNoteContent_PutsTextPlain(t *testing.T) {
	var (
		method string
		ct     string
		path   string
		body   string
	)
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		ct = r.Header.Get("Content-Type")
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.UpdateNoteContent(context.Background(), "ABC", "<p>hi</p>"); err != nil {
		t.Fatalf("UpdateNoteContent: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %s, want PUT", method)
	}
	if ct != "text/plain" {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
	if path != "/etapi/notes/ABC/content" {
		t.Errorf("path = %q", path)
	}
	if body != "<p>hi</p>" {
		t.Errorf("body = %q", body)
	}
}

func TestClient_DeleteNote(t *testing.T) {
	var (
		method string
		path   string
	)
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.DeleteNote(context.Background(), "ABC"); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", method)
	}
	if path != "/etapi/notes/ABC" {
		t.Errorf("path = %q", path)
	}
}

func TestClient_PatchNote(t *testing.T) {
	var patch map[string]any
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		decodeBody(t, r, &patch)
		_, _ = w.Write([]byte(`{"noteId":"ABC","title":"renamed","type":"text"}`))
	})
	n, err := c.PatchNote(context.Background(), "ABC", map[string]any{"title": "renamed"})
	if err != nil {
		t.Fatalf("PatchNote: %v", err)
	}
	if patch["title"] != "renamed" {
		t.Errorf("patch body title = %v", patch["title"])
	}
	if n.Title != "renamed" {
		t.Errorf("response title = %q", n.Title)
	}
}

func TestClient_ErrorResponseSurfaced(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"NOTE_NOT_FOUND","message":"Note 'X' not found."}`))
	})
	_, err := c.GetNote(context.Background(), "X")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "404") || !strings.Contains(msg, "NOTE_NOT_FOUND") {
		t.Errorf("error %q does not surface server message", msg)
	}
}

func TestClient_RespectsContextCancellation(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	if _, err := c.AppInfo(ctx); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestClient_GetNoteContent(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/etapi/notes/N/content" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte("<p>hello</p>"))
	})
	got, err := c.GetNoteContent(context.Background(), "N")
	if err != nil {
		t.Fatalf("GetNoteContent: %v", err)
	}
	if got != "<p>hello</p>" {
		t.Errorf("content = %q", got)
	}
}
