package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: timeout},
	}
}

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

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
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
}

func (c *Client) doText(ctx context.Context, method, path, contentType, body string) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("etapi %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
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
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/etapi/notes/"+url.PathEscape(noteID)+"/content", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("etapi GET content: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return string(data), nil
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
