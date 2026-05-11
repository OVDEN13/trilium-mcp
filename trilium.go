package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	urls  []string
	token string
	http  *http.Client
}

// NewClient accepts one or more Trilium base URLs separated by commas.
// At request time the URLs are tried in order; the first one that produces
// a 2xx response wins. This lets you front a single Trilium with a fast
// LAN address (e.g. http://192.168.0.10:8092) plus a public fallback
// (e.g. https://memo.example.com) without touching code.
func NewClient(baseURLs, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var urls []string
	for _, u := range strings.Split(baseURLs, ",") {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u != "" {
			urls = append(urls, u)
		}
	}
	return &Client{
		urls:  urls,
		token: token,
		http:  &http.Client{Timeout: timeout},
	}
}

// URLs returns the configured base URLs in priority order. Useful for logging.
func (c *Client) URLs() []string { return c.urls }

type Attribute struct {
	AttributeID   string `json:"attributeId,omitempty"`
	NoteID        string `json:"noteId,omitempty"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	Value         string `json:"value"`
	Position      int    `json:"position,omitempty"`
	IsInheritable bool   `json:"isInheritable,omitempty"`
}

type Note struct {
	NoteID         string      `json:"noteId"`
	Title          string      `json:"title"`
	Type           string      `json:"type"`
	Mime           string      `json:"mime,omitempty"`
	IsProtected    bool        `json:"isProtected,omitempty"`
	Attributes     []Attribute `json:"attributes,omitempty"`
	ParentNoteIDs  []string    `json:"parentNoteIds,omitempty"`
	ChildNoteIDs   []string    `json:"childNoteIds,omitempty"`
	DateCreated    string      `json:"dateCreated,omitempty"`
	DateModified   string      `json:"dateModified,omitempty"`
	UtcDateCreated string      `json:"utcDateCreated,omitempty"`
	UtcModified    string      `json:"utcDateModified,omitempty"`
}

type CreateNoteRequest struct {
	ParentNoteID string `json:"parentNoteId"`
	Title        string `json:"title"`
	Type         string `json:"type"`
	Content      string `json:"content"`
	Mime         string `json:"mime,omitempty"`
}

type CreateNoteResponse struct {
	Note   Note `json:"note"`
	Branch struct {
		BranchID     string `json:"branchId"`
		NoteID       string `json:"noteId"`
		ParentNoteID string `json:"parentNoteId"`
	} `json:"branch"`
}

type SearchResponse struct {
	Results []Note `json:"results"`
}

// transportErr marks errors from c.http.Do (network/DNS/TLS) so we know
// they're safe to retry against the next configured URL. HTTP-status errors
// are not transportErr — they're real responses and a different URL won't help.
type transportErr struct{ err error }

func (e transportErr) Error() string { return e.err.Error() }
func (e transportErr) Unwrap() error { return e.err }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyBytes = b
	}
	return c.tryURLs(func(base string) error {
		var rdr io.Reader
		if bodyBytes != nil {
			rdr = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return transportErr{err}
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			return fmt.Errorf("etapi %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	})
}

func (c *Client) doText(ctx context.Context, method, path, contentType, body string) error {
	return c.tryURLs(func(base string) error {
		req, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.token)
		req.Header.Set("Content-Type", contentType)
		resp, err := c.http.Do(req)
		if err != nil {
			return transportErr{err}
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			return fmt.Errorf("etapi %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
		}
		return nil
	})
}

// tryURLs runs fn against each base URL in order, falling back to the next
// one only on transport errors (network/DNS/TLS). HTTP-status errors are
// returned immediately because a different URL won't change them.
func (c *Client) tryURLs(fn func(base string) error) error {
	if len(c.urls) == 0 {
		return fmt.Errorf("no Trilium base URLs configured")
	}
	var lastErr error
	for i, base := range c.urls {
		err := fn(base)
		if err == nil {
			return nil
		}
		var te transportErr
		if errors.As(err, &te) {
			lastErr = err
			if i+1 < len(c.urls) {
				log.Printf("trilium: %s unreachable (%v); trying next URL", base, te.err)
			}
			continue
		}
		// Real server response — fallback won't help.
		return err
	}
	return lastErr
}

func (c *Client) AppInfo(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.do(ctx, "GET", "/etapi/app-info", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateNote(ctx context.Context, req CreateNoteRequest) (*CreateNoteResponse, error) {
	if req.Type == "" {
		req.Type = "text"
	}
	if req.ParentNoteID == "" {
		req.ParentNoteID = "root"
	}
	var out CreateNoteResponse
	if err := c.do(ctx, "POST", "/etapi/create-note", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetNote(ctx context.Context, noteID string) (*Note, error) {
	var out Note
	if err := c.do(ctx, "GET", "/etapi/notes/"+url.PathEscape(noteID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetNoteContent(ctx context.Context, noteID string) (string, error) {
	var out string
	err := c.tryURLs(func(base string) error {
		req, err := http.NewRequestWithContext(ctx, "GET", base+"/etapi/notes/"+url.PathEscape(noteID)+"/content", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", c.token)
		resp, err := c.http.Do(req)
		if err != nil {
			return transportErr{err}
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			return fmt.Errorf("etapi GET content: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		out = string(data)
		return nil
	})
	return out, err
}

func (c *Client) UpdateNoteContent(ctx context.Context, noteID, content string) error {
	return c.doText(ctx, "PUT", "/etapi/notes/"+url.PathEscape(noteID)+"/content", "text/plain", content)
}

func (c *Client) PatchNote(ctx context.Context, noteID string, patch map[string]any) (*Note, error) {
	var out Note
	if err := c.do(ctx, "PATCH", "/etapi/notes/"+url.PathEscape(noteID), patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteNote(ctx context.Context, noteID string) error {
	return c.do(ctx, "DELETE", "/etapi/notes/"+url.PathEscape(noteID), nil, nil)
}

type SearchOpts struct {
	Query            string
	AncestorNoteID   string
	FastSearch       bool
	IncludeArchived  bool
	OrderBy          string
	OrderDirection   string
	Limit            int
	DebugQueryParser bool
}

func (c *Client) SearchNotes(ctx context.Context, opts SearchOpts) ([]Note, error) {
	q := url.Values{}
	q.Set("search", opts.Query)
	if opts.AncestorNoteID != "" {
		q.Set("ancestorNoteId", opts.AncestorNoteID)
	}
	if opts.FastSearch {
		q.Set("fastSearch", "true")
	}
	if opts.IncludeArchived {
		q.Set("includeArchivedNotes", "true")
	}
	if opts.OrderBy != "" {
		q.Set("orderBy", opts.OrderBy)
	}
	if opts.OrderDirection != "" {
		q.Set("orderDirection", opts.OrderDirection)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.DebugQueryParser {
		q.Set("debug", "true")
	}
	var out SearchResponse
	if err := c.do(ctx, "GET", "/etapi/notes?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

func (c *Client) CreateAttribute(ctx context.Context, attr Attribute) (*Attribute, error) {
	if attr.Type == "" {
		attr.Type = "label"
	}
	var out Attribute
	if err := c.do(ctx, "POST", "/etapi/attributes", attr, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteAttribute(ctx context.Context, attributeID string) error {
	return c.do(ctx, "DELETE", "/etapi/attributes/"+url.PathEscape(attributeID), nil, nil)
}

type Branch struct {
	BranchID     string `json:"branchId,omitempty"`
	NoteID       string `json:"noteId"`
	ParentNoteID string `json:"parentNoteId"`
	Prefix       string `json:"prefix,omitempty"`
	NotePosition int    `json:"notePosition,omitempty"`
	IsExpanded   bool   `json:"isExpanded,omitempty"`
}

// CreateBranch attaches an existing note to a new parent. If a branch between
// noteId and parentNoteId already exists, Trilium updates it instead of
// creating a duplicate (returns 200 vs 201; both yield a Branch).
func (c *Client) CreateBranch(ctx context.Context, b Branch) (*Branch, error) {
	var out Branch
	if err := c.do(ctx, "POST", "/etapi/branches", b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteBranch removes a single parent-child link. If the note has other
// parents the note itself stays. If this was the only branch, Trilium deletes
// the note too.
func (c *Client) DeleteBranch(ctx context.Context, branchID string) error {
	return c.do(ctx, "DELETE", "/etapi/branches/"+url.PathEscape(branchID), nil, nil)
}

// BranchID computes Trilium's deterministic branch id from a parent/note pair.
// The format is `<parentNoteId>_<noteId>` — Trilium uses this same convention
// internally (verified empirically and consistent with the public ETAPI).
func BranchID(parentNoteID, noteID string) string {
	return parentNoteID + "_" + noteID
}
