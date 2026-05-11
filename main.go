package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "trilium-mcp"
	serverVersion = "0.1.0"
)

type handlers struct {
	c *Client
}

func main() {
	_ = godotenv.Load()
	baseURL := os.Getenv("TRILIUM_URL")
	token := os.Getenv("TRILIUM_TOKEN")
	if baseURL == "" || token == "" {
		log.Fatal("TRILIUM_URL and TRILIUM_TOKEN must be set (via env or .env file)")
	}

	timeout := 30 * time.Second
	if v := os.Getenv("TRILIUM_HTTP_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}

	h := &handlers{c: NewClient(baseURL, token, timeout)}

	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.c.AppInfo(probeCtx); err != nil {
		log.Printf("warning: Trilium ETAPI probe failed at startup (%v); tool calls may fail until it recovers", err)
	}

	s := server.NewMCPServer(serverName, serverVersion,
		server.WithToolCapabilities(false))

	h.register(s)

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func (h *handlers) register(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("create_note",
		mcp.WithDescription("Create a new note in Trilium. Returns the new note's id and basic metadata."),
		mcp.WithString("title", mcp.Required(), mcp.Description("Note title")),
		mcp.WithString("content", mcp.Description("Note body. For type=text expect HTML; for type=code use raw text.")),
		mcp.WithString("parent_note_id", mcp.Description("Parent note id; defaults to 'root'")),
		mcp.WithString("type", mcp.Description("Note type: text|code|book|relationMap|... default text")),
		mcp.WithString("mime", mcp.Description("MIME type, e.g. text/html, text/markdown, application/json")),
		mcp.WithObject("labels", mcp.Description("Map of label name->value to attach immediately after creation")),
	), h.createNote)

	s.AddTool(mcp.NewTool("get_note",
		mcp.WithDescription("Fetch a note's metadata and (optionally) its body content."),
		mcp.WithString("note_id", mcp.Required(), mcp.Description("Note id")),
		mcp.WithBoolean("include_content", mcp.Description("Include body content (default false)")),
	), h.getNote)

	s.AddTool(mcp.NewTool("update_note",
		mcp.WithDescription("Update a note's title, type, or replace its body content. Omit a field to keep it; pass an explicit empty string to clear it."),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("title", mcp.Description("New title")),
		mcp.WithString("type", mcp.Description("New type")),
		mcp.WithString("content", mcp.Description("Replace body with this content")),
	), h.updateNote)

	s.AddTool(mcp.NewTool("append_content",
		mcp.WithDescription("Append text to a note's existing body, separated by a configurable separator."),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("content", mcp.Required(), mcp.Description("Text to append")),
		mcp.WithString("separator", mcp.Description("Separator between old and new content (default \\n\\n)")),
	), h.appendContent)

	s.AddTool(mcp.NewTool("delete_note",
		mcp.WithDescription("Delete a note (and its subtree)."),
		mcp.WithString("note_id", mcp.Required()),
	), h.deleteNote)

	s.AddTool(mcp.NewTool("search_notes",
		mcp.WithDescription("Search notes using Trilium search syntax (e.g. '#tag', '#status=active', '\"foo bar\"', 'note.title %= \"^Re\"'). Returns up to 'limit' results."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Trilium search expression")),
		mcp.WithString("ancestor_note_id", mcp.Description("Restrict search to descendants of this note")),
		mcp.WithBoolean("fast_search", mcp.Description("Skip full-text body scan, search metadata only (default false)")),
		mcp.WithBoolean("include_archived", mcp.Description("Include archived notes (default false)")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 50)")),
	), h.searchNotes)

	s.AddTool(mcp.NewTool("add_label",
		mcp.WithDescription("Attach a label (key[=value]) to a note. Labels act as table columns in collection views."),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("name", mcp.Required(), mcp.Description("Label name (without the leading #)")),
		mcp.WithString("value", mcp.Description("Optional label value")),
		mcp.WithBoolean("inheritable", mcp.Description("If true, child notes inherit this label")),
	), h.addLabel)

	s.AddTool(mcp.NewTool("add_relation",
		mcp.WithDescription("Add a relation from one note to another (like a foreign key to another 'row')."),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("name", mcp.Required(), mcp.Description("Relation name (without the leading ~)")),
		mcp.WithString("target_note_id", mcp.Required(), mcp.Description("Target note id")),
		mcp.WithBoolean("inheritable", mcp.Description("If true, child notes inherit this relation")),
	), h.addRelation)

	s.AddTool(mcp.NewTool("remove_attribute",
		mcp.WithDescription("Remove a label or relation by its attribute id."),
		mcp.WithString("attribute_id", mcp.Required()),
	), h.removeAttribute)

	s.AddTool(mcp.NewTool("list_attributes",
		mcp.WithDescription("List all labels and relations on a note."),
		mcp.WithString("note_id", mcp.Required()),
	), h.listAttributes)
}

func okJSON(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("failed to marshal result: " + err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

func errResult(format string, args ...any) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(fmt.Sprintf(format, args...)), nil
}

func argString(req mcp.CallToolRequest, name string) string {
	if v, ok := req.GetArguments()[name]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func argStringPresent(req mcp.CallToolRequest, name string) (string, bool) {
	v, ok := req.GetArguments()[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func argBool(req mcp.CallToolRequest, name string) bool {
	if v, ok := req.GetArguments()[name]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func argInt(req mcp.CallToolRequest, name string, def int) int {
	v, ok := req.GetArguments()[name]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return def
}

func argStringMap(req mcp.CallToolRequest, name string) map[string]string {
	v, ok := req.GetArguments()[name]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		switch s := val.(type) {
		case string:
			out[k] = s
		case nil:
			out[k] = ""
		case bool:
			out[k] = strconv.FormatBool(s)
		case float64:
			out[k] = strconv.FormatFloat(s, 'f', -1, 64)
		default:
			b, _ := json.Marshal(s)
			out[k] = string(b)
		}
	}
	return out
}

func (h *handlers) createNote(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	title := argString(req, "title")
	if title == "" {
		return errResult("'title' is required")
	}
	creq := CreateNoteRequest{
		Title:        title,
		Content:      argString(req, "content"),
		ParentNoteID: argString(req, "parent_note_id"),
		Type:         argString(req, "type"),
		Mime:         argString(req, "mime"),
	}
	resp, err := h.c.CreateNote(ctx, creq)
	if err != nil {
		return errResult("create_note failed: %v", err)
	}
	labels := argStringMap(req, "labels")
	for k, v := range labels {
		if _, err := h.c.CreateAttribute(ctx, Attribute{
			NoteID: resp.Note.NoteID, Type: "label", Name: k, Value: v,
		}); err != nil {
			return errResult("note %s created but label %q failed: %v", resp.Note.NoteID, k, err)
		}
	}
	return okJSON(map[string]any{
		"note_id":         resp.Note.NoteID,
		"title":           resp.Note.Title,
		"type":            resp.Note.Type,
		"parent_note_id":  resp.Branch.ParentNoteID,
		"branch_id":       resp.Branch.BranchID,
		"labels_attached": labels,
	})
}

func (h *handlers) getNote(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "note_id")
	if id == "" {
		return errResult("'note_id' is required")
	}
	note, err := h.c.GetNote(ctx, id)
	if err != nil {
		return errResult("get_note failed: %v", err)
	}
	out := map[string]any{"note": note}
	if argBool(req, "include_content") {
		c, err := h.c.GetNoteContent(ctx, id)
		if err != nil {
			return errResult("get_note: content fetch failed: %v", err)
		}
		out["content"] = c
	}
	return okJSON(out)
}

func (h *handlers) updateNote(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "note_id")
	if id == "" {
		return errResult("'note_id' is required")
	}
	patch := map[string]any{}
	if v, ok := argStringPresent(req, "title"); ok {
		patch["title"] = v
	}
	if v, ok := argStringPresent(req, "type"); ok {
		patch["type"] = v
	}
	var updated *Note
	if len(patch) > 0 {
		n, err := h.c.PatchNote(ctx, id, patch)
		if err != nil {
			return errResult("update_note: patch failed: %v", err)
		}
		updated = n
	}
	if content, ok := argStringPresent(req, "content"); ok {
		if err := h.c.UpdateNoteContent(ctx, id, content); err != nil {
			return errResult("update_note: content update failed: %v", err)
		}
	}
	if updated == nil {
		n, err := h.c.GetNote(ctx, id)
		if err != nil {
			return errResult("update_note: post-update fetch failed: %v", err)
		}
		updated = n
	}
	return okJSON(updated)
}

func (h *handlers) appendContent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "note_id")
	if id == "" {
		return errResult("'note_id' is required")
	}
	add := argString(req, "content")
	if add == "" {
		return errResult("'content' is required")
	}
	sep := argString(req, "separator")
	if sep == "" {
		sep = "\n\n"
	}
	existing, err := h.c.GetNoteContent(ctx, id)
	if err != nil {
		return errResult("append_content: fetch failed: %v", err)
	}
	combined := existing
	if combined != "" {
		combined += sep
	}
	combined += add
	if err := h.c.UpdateNoteContent(ctx, id, combined); err != nil {
		return errResult("append_content: write failed: %v", err)
	}
	return okJSON(map[string]any{"note_id": id, "bytes_written": len(combined)})
}

func (h *handlers) deleteNote(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "note_id")
	if id == "" {
		return errResult("'note_id' is required")
	}
	if err := h.c.DeleteNote(ctx, id); err != nil {
		return errResult("delete_note failed: %v", err)
	}
	return okJSON(map[string]any{"deleted": id})
}

func (h *handlers) searchNotes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q := argString(req, "query")
	if q == "" {
		return errResult("'query' is required")
	}
	opts := SearchOpts{
		Query:           q,
		AncestorNoteID:  argString(req, "ancestor_note_id"),
		FastSearch:      argBool(req, "fast_search"),
		IncludeArchived: argBool(req, "include_archived"),
		Limit:           argInt(req, "limit", 50),
	}
	notes, err := h.c.SearchNotes(ctx, opts)
	if err != nil {
		return errResult("search_notes failed: %v", err)
	}
	type slim struct {
		NoteID     string      `json:"note_id"`
		Title      string      `json:"title"`
		Type       string      `json:"type"`
		Attributes []Attribute `json:"attributes,omitempty"`
	}
	out := make([]slim, 0, len(notes))
	for _, n := range notes {
		out = append(out, slim{NoteID: n.NoteID, Title: n.Title, Type: n.Type, Attributes: n.Attributes})
	}
	return okJSON(map[string]any{"count": len(out), "results": out})
}

func (h *handlers) addLabel(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "note_id")
	name := argString(req, "name")
	if id == "" || name == "" {
		return errResult("'note_id' and 'name' are required")
	}
	attr, err := h.c.CreateAttribute(ctx, Attribute{
		NoteID:        id,
		Type:          "label",
		Name:          name,
		Value:         argString(req, "value"),
		IsInheritable: argBool(req, "inheritable"),
	})
	if err != nil {
		return errResult("add_label failed: %v", err)
	}
	return okJSON(attr)
}

func (h *handlers) addRelation(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "note_id")
	name := argString(req, "name")
	target := argString(req, "target_note_id")
	if id == "" || name == "" || target == "" {
		return errResult("'note_id', 'name' and 'target_note_id' are required")
	}
	attr, err := h.c.CreateAttribute(ctx, Attribute{
		NoteID:        id,
		Type:          "relation",
		Name:          name,
		Value:         target,
		IsInheritable: argBool(req, "inheritable"),
	})
	if err != nil {
		return errResult("add_relation failed: %v", err)
	}
	return okJSON(attr)
}

func (h *handlers) removeAttribute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "attribute_id")
	if id == "" {
		return errResult("'attribute_id' is required")
	}
	if err := h.c.DeleteAttribute(ctx, id); err != nil {
		return errResult("remove_attribute failed: %v", err)
	}
	return okJSON(map[string]any{"deleted": id})
}

func (h *handlers) listAttributes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "note_id")
	if id == "" {
		return errResult("'note_id' is required")
	}
	note, err := h.c.GetNote(ctx, id)
	if err != nil {
		return errResult("list_attributes failed: %v", err)
	}
	return okJSON(map[string]any{
		"note_id":    note.NoteID,
		"attributes": note.Attributes,
	})
}
