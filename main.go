package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "trilium-mcp"
	serverVersion = "0.1.5"
)

type logLevel int

const (
	logOff logLevel = iota
	logInfo
	logDebug
)

func parseLogLevel(s string) logLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "0", "false", "no":
		return logOff
	case "debug", "verbose", "2":
		return logDebug
	default:
		return logInfo
	}
}

type handlers struct {
	c   *Client
	lvl logLevel
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

	lvl := parseLogLevel(os.Getenv("TRILIUM_MCP_LOG"))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("[trilium-mcp] ")

	h := &handlers{c: NewClient(baseURL, token, timeout), lvl: lvl}

	if lvl != logOff {
		log.Printf("starting %s v%s — trilium=%s timeout=%s log=%s", serverName, serverVersion, strings.Join(h.c.URLs(), ","), timeout, logLevelName(lvl))
	}

	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.c.AppInfo(probeCtx); err != nil {
		log.Printf("warning: Trilium ETAPI probe failed at startup (%v); tool calls may fail until it recovers", err)
	}

	s := server.NewMCPServer(serverName, serverVersion,
		server.WithToolCapabilities(false))

	h.register(s)

	// mcp-go's stdio writer uses json.Marshal directly, which HTML-escapes <, >, &
	// into <, >, &. For an MCP that serves HTML-bodied notes this
	// roughly inflates response size by 15%. We can't switch the encoder inside
	// mcp-go, so we wrap stdout with a streaming replacer that converts those
	// sequences back. Both forms are valid JSON encodings of the same character,
	// so the receiving client sees identical data.
	ss := server.NewStdioServer(s)
	if err := ss.Listen(context.Background(), os.Stdin, &htmlUnescapingWriter{w: os.Stdout}); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

type htmlUnescapingWriter struct{ w *os.File }

func (h *htmlUnescapingWriter) Write(p []byte) (int, error) {
	// p contains the literal 6-byte sequences <, >, & — these are
	// what Go's json package emits when HTML escape is on (its default).
	// Replace them with the single bytes <, >, &. JSON-equivalent, ~15% smaller.
	out := bytes.ReplaceAll(p, []byte("\\u003c"), []byte("<"))
	out = bytes.ReplaceAll(out, []byte("\\u003e"), []byte(">"))
	out = bytes.ReplaceAll(out, []byte("\\u0026"), []byte("&"))
	if _, err := h.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

func logLevelName(l logLevel) string {
	switch l {
	case logOff:
		return "off"
	case logDebug:
		return "debug"
	default:
		return "info"
	}
}

func (h *handlers) withLogging(name string, fn server.ToolHandlerFunc) server.ToolHandlerFunc {
	if h.lvl == logOff {
		return fn
	}
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		if h.lvl >= logDebug {
			args, _ := json.Marshal(req.GetArguments())
			log.Printf("→ %s args=%s", name, truncate(string(args), 300))
		} else {
			log.Printf("→ %s", name)
		}
		result, err := fn(ctx, req)
		dur := time.Since(start).Round(time.Microsecond)
		switch {
		case err != nil:
			log.Printf("← %s exec-error in %s: %v", name, dur, err)
		case result != nil && result.IsError:
			log.Printf("← %s tool-error in %s: %s", name, dur, summarizeResult(result, 160))
		case h.lvl >= logDebug:
			log.Printf("← %s ok in %s: %s", name, dur, summarizeResult(result, 300))
		default:
			log.Printf("← %s ok in %s", name, dur)
		}
		return result, err
	}
}

func summarizeResult(r *mcp.CallToolResult, max int) string {
	if r == nil {
		return ""
	}
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return truncate(strings.ReplaceAll(tc.Text, "\n", " "), max)
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func (h *handlers) register(s *server.MCPServer) {
	readOnly := mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(true),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(true),
	}
	additive := mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(false),
		OpenWorldHint:   boolPtr(true),
	}
	destructive := mcp.ToolAnnotation{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(true),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(true),
	}

	s.AddTool(mcp.NewTool("create_note",
		mcp.WithDescription("Create a new note in Trilium. Returns the new note's id and basic metadata."),
		mcp.WithToolAnnotation(additive),
		mcp.WithString("title", mcp.Required(), mcp.Description("Note title")),
		mcp.WithString("content", mcp.Description("Note body. For type=text expect HTML; for type=code use raw text.")),
		mcp.WithString("parent_note_id", mcp.Description("Parent note id; defaults to 'root'")),
		mcp.WithString("type", mcp.Description("Note type: text|code|book|relationMap|... default text")),
		mcp.WithString("mime", mcp.Description("MIME type, e.g. text/html, text/markdown, application/json")),
		mcp.WithObject("labels", mcp.Description("Map of label name -> value to attach immediately after creation. Pass as a JSON object, e.g. {\"host\":\"mac-mini\",\"category\":\"runbooks\"} — NOT as a stringified JSON.")),
	), h.withLogging("create_note", h.createNote))

	s.AddTool(mcp.NewTool("get_note",
		mcp.WithDescription("Fetch a note's metadata and (optionally) its body content."),
		mcp.WithToolAnnotation(readOnly),
		mcp.WithString("note_id", mcp.Required(), mcp.Description("Note id")),
		mcp.WithBoolean("include_content", mcp.Description("Include body content (default false)")),
	), h.withLogging("get_note", h.getNote))

	s.AddTool(mcp.NewTool("update_note",
		mcp.WithDescription("Patch a note's title, type, and/or replace its body content. Partial: any field you OMIT stays unchanged; any field you INCLUDE (even with an empty string) is applied. To update only the title, send {note_id, title} and skip content."),
		mcp.WithToolAnnotation(destructive),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("title", mcp.Description("New title")),
		mcp.WithString("type", mcp.Description("New type")),
		mcp.WithString("content", mcp.Description("Replace body with this content")),
	), h.withLogging("update_note", h.updateNote))

	s.AddTool(mcp.NewTool("append_content",
		mcp.WithDescription("Append text to a note's existing body, separated by a configurable separator."),
		mcp.WithToolAnnotation(additive),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("content", mcp.Required(), mcp.Description("Text to append")),
		mcp.WithString("separator", mcp.Description("Separator between old and new content (default \\n\\n)")),
	), h.withLogging("append_content", h.appendContent))

	s.AddTool(mcp.NewTool("delete_note",
		mcp.WithDescription("Delete a note (and its subtree)."),
		mcp.WithToolAnnotation(destructive),
		mcp.WithString("note_id", mcp.Required()),
	), h.withLogging("delete_note", h.deleteNote))

	s.AddTool(mcp.NewTool("search_notes",
		mcp.WithDescription("Search notes using Trilium search syntax (e.g. '#tag', '#status=active', '\"foo bar\"', 'note.title %= \"^Re\"'). Returns up to 'limit' results."),
		mcp.WithToolAnnotation(readOnly),
		mcp.WithString("query", mcp.Required(), mcp.Description("Trilium search expression")),
		mcp.WithString("ancestor_note_id", mcp.Description("Scope the search to one subtree — only notes that are descendants of this note are returned. This is the correct way to limit search to a 'folder' like '🔧 Runbooks'.")),
		mcp.WithBoolean("fast_search", mcp.Description("Skip full-text body scan, search metadata only (default false)")),
		mcp.WithBoolean("include_archived", mcp.Description("Include archived notes (default false)")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 50)")),
	), h.withLogging("search_notes", h.searchNotes))

	s.AddTool(mcp.NewTool("add_label",
		mcp.WithDescription("Attach a label (key[=value]) to a note. Labels act as table columns in collection views."),
		mcp.WithToolAnnotation(additive),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("name", mcp.Required(), mcp.Description("Label name (without the leading #)")),
		mcp.WithString("value", mcp.Description("Optional label value")),
		mcp.WithBoolean("inheritable", mcp.Description("If true, child notes inherit this label")),
	), h.withLogging("add_label", h.addLabel))

	s.AddTool(mcp.NewTool("add_relation",
		mcp.WithDescription("Add a relation from one note to another (like a foreign key to another 'row')."),
		mcp.WithToolAnnotation(additive),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("name", mcp.Required(), mcp.Description("Relation name (without the leading ~)")),
		mcp.WithString("target_note_id", mcp.Required(), mcp.Description("Target note id")),
		mcp.WithBoolean("inheritable", mcp.Description("If true, child notes inherit this relation")),
	), h.withLogging("add_relation", h.addRelation))

	s.AddTool(mcp.NewTool("remove_attribute",
		mcp.WithDescription("Remove a label or relation by its attribute id."),
		mcp.WithToolAnnotation(destructive),
		mcp.WithString("attribute_id", mcp.Required()),
	), h.withLogging("remove_attribute", h.removeAttribute))

	s.AddTool(mcp.NewTool("list_attributes",
		mcp.WithDescription("List all labels and relations on a note."),
		mcp.WithToolAnnotation(readOnly),
		mcp.WithString("note_id", mcp.Required()),
	), h.withLogging("list_attributes", h.listAttributes))

	s.AddTool(mcp.NewTool("move_note",
		mcp.WithDescription("Move a note from one parent to another by re-parenting its branch. Performs two ETAPI calls (create branch under new parent, delete old branch) — cheap compared to get+recreate+delete. If the note has multiple parents, you must pass from_parent_id; otherwise the single existing parent is used automatically."),
		mcp.WithToolAnnotation(destructive),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("new_parent_id", mcp.Required(), mcp.Description("The new parent note id")),
		mcp.WithString("from_parent_id", mcp.Description("Required only if the note has multiple parents; pick the parent you want to detach from")),
		mcp.WithNumber("position", mcp.Description("Optional position among siblings under the new parent (Trilium uses 10/20/30 spacing)")),
	), h.withLogging("move_note", h.moveNote))

	s.AddTool(mcp.NewTool("clone_note",
		mcp.WithDescription("Add the note under an additional parent without removing it from existing parents (Trilium 'clone' = shared note across multiple tree locations). One ETAPI call."),
		mcp.WithToolAnnotation(additive),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithString("new_parent_id", mcp.Required(), mcp.Description("The parent under which to attach this note as a clone")),
		mcp.WithString("prefix", mcp.Description("Optional per-branch prefix shown in the tree (e.g. 'see also:')")),
		mcp.WithNumber("position", mcp.Description("Optional position among siblings under the new parent")),
	), h.withLogging("clone_note", h.cloneNote))

	s.AddTool(mcp.NewTool("delete_branch",
		mcp.WithDescription("Delete a single parent-child link (one Trilium branch) without deleting the note. Use this to un-clone — i.e. remove a note from one of its parents while keeping it in the others. If the deleted branch was the note's last one, Trilium deletes the note itself."),
		mcp.WithToolAnnotation(destructive),
		mcp.WithString("branch_id", mcp.Required(), mcp.Description("Branch id, formatted as `<parentNoteId>_<noteId>`")),
	), h.withLogging("delete_branch", h.deleteBranch))

	s.AddTool(mcp.NewTool("batch_create_notes",
		mcp.WithDescription("Create many notes in a single tool call. Each item has the same fields as create_note (title, content, parent_note_id, type, mime, labels). Use when restructuring or seeding a section — saves the per-call schema overhead an agent pays for repeated create_note calls."),
		mcp.WithToolAnnotation(additive),
		mcp.WithArray("notes", mcp.Required(), mcp.Description("Array of note specs. Each: {title (required), content?, parent_note_id?, type?, mime?, labels?}")),
	), h.withLogging("batch_create_notes", h.batchCreateNotes))

	s.AddTool(mcp.NewTool("batch_delete_notes",
		mcp.WithDescription("Delete many notes by id. Each note is attempted independently; the response reports `deleted` and `failed` arrays so partial failures don't stop the rest."),
		mcp.WithToolAnnotation(destructive),
		mcp.WithArray("note_ids", mcp.Required(), mcp.Description("Array of note ids to delete")),
	), h.withLogging("batch_delete_notes", h.batchDeleteNotes))

	s.AddTool(mcp.NewTool("get_note_subtree",
		mcp.WithDescription("Recursively fetch a note plus its descendants up to max_depth levels as a single nested tree — replaces N+1 get_note calls when navigating a section. Bodies are NOT included by default (saves tokens); set include_content=true if you need them. Attributes are included by default."),
		mcp.WithToolAnnotation(readOnly),
		mcp.WithString("note_id", mcp.Required()),
		mcp.WithNumber("max_depth", mcp.Description("How deep to recurse. 0 = just the root note. 1 = root + direct children. Default 2.")),
		mcp.WithBoolean("include_content", mcp.Description("Include body content of each note (default false — saves tokens)")),
		mcp.WithNumber("limit", mcp.Description("Hard cap on total notes returned (default 200) — protects against accidental fetches of huge trees")),
	), h.withLogging("get_note_subtree", h.getNoteSubtree))
}

func boolPtr(b bool) *bool { return &b }

func okJSON(v any) (*mcp.CallToolResult, error) {
	// SetEscapeHTML(false) keeps raw <, >, &, in the output instead of the
	// < > & forms that encoding/json defaults to. This matters
	// for AI agents reading note HTML — escaped output costs ~15% extra tokens
	// and is harder to scan.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return mcp.NewToolResultError("failed to marshal result: " + err.Error()), nil
	}
	// json.Encoder appends a trailing newline — drop it.
	out := strings.TrimRight(buf.String(), "\n")
	return mcp.NewToolResultText(out), nil
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

func (h *handlers) moveNote(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	noteID := argString(req, "note_id")
	newParent := argString(req, "new_parent_id")
	if noteID == "" || newParent == "" {
		return errResult("'note_id' and 'new_parent_id' are required")
	}
	fromParent := argString(req, "from_parent_id")
	if fromParent == "" {
		// Look up the note's parents and require unambiguity.
		note, err := h.c.GetNote(ctx, noteID)
		if err != nil {
			return errResult("move_note: failed to read note: %v", err)
		}
		switch len(note.ParentNoteIDs) {
		case 0:
			return errResult("move_note: note %s has no parents — nothing to move from", noteID)
		case 1:
			fromParent = note.ParentNoteIDs[0]
		default:
			return errResult("move_note: note %s has %d parents (%v) — pass from_parent_id to disambiguate", noteID, len(note.ParentNoteIDs), note.ParentNoteIDs)
		}
	}
	if fromParent == newParent {
		return errResult("move_note: from_parent_id and new_parent_id are the same (%s)", newParent)
	}
	// Create the new branch first, then drop the old one. If create fails we
	// haven't broken anything; if delete fails the note temporarily has both
	// parents (a clone), which is recoverable.
	b := Branch{NoteID: noteID, ParentNoteID: newParent}
	b.NotePosition = argInt(req, "position", 0)
	created, err := h.c.CreateBranch(ctx, b)
	if err != nil {
		return errResult("move_note: create new branch failed: %v", err)
	}
	if err := h.c.DeleteBranch(ctx, BranchID(fromParent, noteID)); err != nil {
		return errResult("move_note: branch attached to new parent (%s) but failed to remove old branch from %s: %v", created.BranchID, fromParent, err)
	}
	return okJSON(map[string]any{
		"note_id":          noteID,
		"new_parent_id":    newParent,
		"old_parent_id":    fromParent,
		"new_branch_id":    created.BranchID,
		"removed_branch":   BranchID(fromParent, noteID),
	})
}

func (h *handlers) cloneNote(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	noteID := argString(req, "note_id")
	newParent := argString(req, "new_parent_id")
	if noteID == "" || newParent == "" {
		return errResult("'note_id' and 'new_parent_id' are required")
	}
	b := Branch{
		NoteID:       noteID,
		ParentNoteID: newParent,
		Prefix:       argString(req, "prefix"),
		NotePosition: argInt(req, "position", 0),
	}
	out, err := h.c.CreateBranch(ctx, b)
	if err != nil {
		return errResult("clone_note failed: %v", err)
	}
	return okJSON(out)
}

func (h *handlers) deleteBranch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := argString(req, "branch_id")
	if id == "" {
		return errResult("'branch_id' is required")
	}
	if err := h.c.DeleteBranch(ctx, id); err != nil {
		return errResult("delete_branch failed: %v", err)
	}
	return okJSON(map[string]any{"deleted": id})
}

func (h *handlers) batchCreateNotes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, ok := req.GetArguments()["notes"]
	if !ok {
		return errResult("'notes' is required")
	}
	list, ok := raw.([]any)
	if !ok {
		return errResult("'notes' must be an array")
	}
	type result struct {
		Index        int               `json:"index"`
		NoteID       string            `json:"note_id,omitempty"`
		Title        string            `json:"title,omitempty"`
		BranchID     string            `json:"branch_id,omitempty"`
		ParentNoteID string            `json:"parent_note_id,omitempty"`
		Labels       map[string]string `json:"labels,omitempty"`
		Error        string            `json:"error,omitempty"`
	}
	results := make([]result, 0, len(list))
	for i, item := range list {
		spec, ok := item.(map[string]any)
		if !ok {
			results = append(results, result{Index: i, Error: "not an object"})
			continue
		}
		title, _ := spec["title"].(string)
		if title == "" {
			results = append(results, result{Index: i, Error: "'title' is required"})
			continue
		}
		creq := CreateNoteRequest{
			Title:        title,
			Content:      strFrom(spec["content"]),
			ParentNoteID: strFrom(spec["parent_note_id"]),
			Type:         strFrom(spec["type"]),
			Mime:         strFrom(spec["mime"]),
		}
		resp, err := h.c.CreateNote(ctx, creq)
		if err != nil {
			results = append(results, result{Index: i, Title: title, Error: err.Error()})
			continue
		}
		var labels map[string]string
		if rawLabels, ok := spec["labels"].(map[string]any); ok && len(rawLabels) > 0 {
			labels = make(map[string]string, len(rawLabels))
			for k, v := range rawLabels {
				labels[k] = coerceString(v)
				if _, err := h.c.CreateAttribute(ctx, Attribute{
					NoteID: resp.Note.NoteID, Type: "label", Name: k, Value: labels[k],
				}); err != nil {
					results = append(results, result{
						Index: i, Title: title, NoteID: resp.Note.NoteID, BranchID: resp.Branch.BranchID, ParentNoteID: resp.Branch.ParentNoteID, Labels: labels,
						Error: fmt.Sprintf("note created but label %q failed: %v", k, err),
					})
					goto next
				}
			}
		}
		results = append(results, result{
			Index: i, Title: title, NoteID: resp.Note.NoteID, BranchID: resp.Branch.BranchID,
			ParentNoteID: resp.Branch.ParentNoteID, Labels: labels,
		})
	next:
	}
	created, failed := 0, 0
	for _, r := range results {
		if r.Error == "" {
			created++
		} else {
			failed++
		}
	}
	return okJSON(map[string]any{
		"created": created,
		"failed":  failed,
		"results": results,
	})
}

func (h *handlers) batchDeleteNotes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, ok := req.GetArguments()["note_ids"]
	if !ok {
		return errResult("'note_ids' is required")
	}
	list, ok := raw.([]any)
	if !ok {
		return errResult("'note_ids' must be an array of strings")
	}
	deleted := make([]string, 0, len(list))
	failed := make([]map[string]string, 0)
	for _, item := range list {
		id, ok := item.(string)
		if !ok || id == "" {
			failed = append(failed, map[string]string{"id": fmt.Sprintf("%v", item), "error": "not a non-empty string"})
			continue
		}
		if err := h.c.DeleteNote(ctx, id); err != nil {
			failed = append(failed, map[string]string{"id": id, "error": err.Error()})
			continue
		}
		deleted = append(deleted, id)
	}
	return okJSON(map[string]any{
		"deleted": deleted,
		"failed":  failed,
	})
}

func (h *handlers) getNoteSubtree(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	rootID := argString(req, "note_id")
	if rootID == "" {
		return errResult("'note_id' is required")
	}
	maxDepth := argInt(req, "max_depth", 2)
	if maxDepth < 0 {
		maxDepth = 0
	}
	limit := argInt(req, "limit", 200)
	if limit <= 0 {
		limit = 200
	}
	includeContent := argBool(req, "include_content")

	type node struct {
		NoteID     string      `json:"note_id"`
		Title      string      `json:"title"`
		Type       string      `json:"type"`
		Attributes []Attribute `json:"attributes,omitempty"`
		Content    string      `json:"content,omitempty"`
		Children   []*node     `json:"children,omitempty"`
		Truncated  bool        `json:"truncated_at_depth,omitempty"`
	}

	count := 0
	var visit func(id string, depth int) (*node, error)
	visit = func(id string, depth int) (*node, error) {
		if count >= limit {
			return nil, nil
		}
		count++
		n, err := h.c.GetNote(ctx, id)
		if err != nil {
			return nil, err
		}
		out := &node{NoteID: n.NoteID, Title: n.Title, Type: n.Type, Attributes: n.Attributes}
		if includeContent {
			c, err := h.c.GetNoteContent(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("get_note_subtree: content fetch for %s failed: %w", id, err)
			}
			out.Content = c
		}
		if depth >= maxDepth {
			if len(n.ChildNoteIDs) > 0 {
				out.Truncated = true
			}
			return out, nil
		}
		for _, childID := range n.ChildNoteIDs {
			if count >= limit {
				out.Truncated = true
				break
			}
			child, err := visit(childID, depth+1)
			if err != nil {
				return nil, err
			}
			if child != nil {
				out.Children = append(out.Children, child)
			}
		}
		return out, nil
	}
	root, err := visit(rootID, 0)
	if err != nil {
		return errResult("get_note_subtree failed: %v", err)
	}
	return okJSON(map[string]any{
		"root":          root,
		"notes_visited": count,
		"limit":         limit,
		"max_depth":     maxDepth,
	})
}

func strFrom(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func coerceString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(s)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	default:
		b, _ := json.Marshal(s)
		return string(b)
	}
}
