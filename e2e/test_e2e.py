#!/usr/bin/env python3
"""End-to-end test for trilium-mcp. Drives the binary over stdio."""
import json, subprocess, sys, os, threading
from queue import Queue, Empty

BIN = os.environ.get("MCP_BIN", "./trilium-mcp")

class Mcp:
    def __init__(self):
        self.p = subprocess.Popen([BIN], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=1, text=True)
        self.q = Queue()
        threading.Thread(target=self._reader, daemon=True).start()
        self.nid = 0

    def _reader(self):
        for line in self.p.stdout:
            line = line.strip()
            if line:
                self.q.put(json.loads(line))

    def call(self, method, params=None, want_response=True):
        self.nid += 1
        req = {"jsonrpc": "2.0", "method": method, "params": params or {}}
        if want_response:
            req["id"] = self.nid
        self.p.stdin.write(json.dumps(req) + "\n")
        self.p.stdin.flush()
        if not want_response:
            return None
        try:
            return self.q.get(timeout=15)
        except Empty:
            raise RuntimeError(f"timeout waiting for response to {method}")

    def tool(self, name, args):
        resp = self.call("tools/call", {"name": name, "arguments": args})
        if "error" in resp:
            raise RuntimeError(f"{name} RPC error: {resp['error']}")
        result = resp["result"]
        if result.get("isError"):
            text = result["content"][0]["text"]
            raise RuntimeError(f"{name} tool error: {text}")
        return json.loads(result["content"][0]["text"])

    def close(self):
        self.p.stdin.close()
        self.p.wait(timeout=5)

def section(t): print(f"\n=== {t} ===")
def ok(t): print(f"  ✓ {t}")

def main():
    m = Mcp()
    failures = []
    try:
        section("initialize + tools/list")
        init = m.call("initialize", {"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}})
        assert init["result"]["serverInfo"]["name"] == "trilium-mcp"
        ok(f"server: {init['result']['serverInfo']['name']} v{init['result']['serverInfo']['version']}")
        m.call("notifications/initialized", want_response=False)
        tools = m.call("tools/list")["result"]["tools"]
        names = sorted(t["name"] for t in tools)
        expected = sorted(["create_note","get_note","update_note","append_content","delete_note","search_notes","add_label","add_relation","remove_attribute","list_attributes"])
        assert names == expected, f"missing tools: {set(expected)-set(names)}"
        ok(f"all 10 tools registered: {names}")

        section("create_note (with labels)")
        a = m.tool("create_note", {"title":"e2e note A","content":"<p>row A</p>","labels":{"e2e":"full","kind":"a"}})
        assert a["title"] == "e2e note A"
        note_a = a["note_id"]
        ok(f"note A created: {note_a}, labels={a['labels_attached']}")

        section("create_note (no labels, second note for relation target)")
        b = m.tool("create_note", {"title":"e2e note B","content":"<p>target B</p>"})
        note_b = b["note_id"]
        ok(f"note B created: {note_b}")

        section("add_label (separately)")
        lbl = m.tool("add_label", {"note_id": note_a, "name":"priority","value":"high"})
        assert lbl["name"] == "priority"
        ok(f"label added: id={lbl['attributeId']} priority=high")
        lbl_id = lbl["attributeId"]

        section("add_relation A -> B")
        rel = m.tool("add_relation", {"note_id": note_a, "name":"linked_to","target_note_id": note_b})
        assert rel["value"] == note_b
        ok(f"relation added: id={rel['attributeId']} linked_to -> {note_b}")

        section("list_attributes on A")
        attrs = m.tool("list_attributes", {"note_id": note_a})
        attr_names = sorted([(x["type"], x["name"]) for x in attrs["attributes"]])
        expected_names = sorted([("label","e2e"),("label","kind"),("label","priority"),("relation","linked_to")])
        assert attr_names == expected_names, f"got {attr_names}, want {expected_names}"
        ok(f"4 attributes present: {attr_names}")

        section("search_notes by label")
        s1 = m.tool("search_notes", {"query":"#e2e=full","limit":10})
        ids = [r["note_id"] for r in s1["results"]]
        assert note_a in ids, f"A {note_a} not in {ids}"
        ok(f"search '#e2e=full' found A: count={s1['count']}")

        section("search_notes scoped to ancestor")
        s2 = m.tool("search_notes", {"query":"#kind","ancestor_note_id":"root","limit":10})
        ok(f"scoped search returned {s2['count']} results")

        section("get_note (metadata only)")
        g1 = m.tool("get_note", {"note_id": note_a})
        assert "content" not in g1
        assert g1["note"]["title"] == "e2e note A"
        ok("metadata returned, no content field")

        section("get_note (include_content=true)")
        g2 = m.tool("get_note", {"note_id": note_a, "include_content": True})
        assert "<p>row A</p>" in g2["content"]
        ok(f"content fetched: {len(g2['content'])} bytes")

        section("update_note (title only)")
        u1 = m.tool("update_note", {"note_id": note_a, "title":"e2e note A (renamed)"})
        assert u1["title"] == "e2e note A (renamed)"
        ok("title updated, content untouched")

        section("update_note (replace content)")
        u2 = m.tool("update_note", {"note_id": note_a, "content":"<p>completely new body</p>"})
        ok("content replaced")
        g3 = m.tool("get_note", {"note_id": note_a, "include_content": True})
        assert "completely new body" in g3["content"], f"got: {g3['content']}"
        assert "row A" not in g3["content"]
        ok("verified: new body present, old body gone")

        section("append_content")
        ap = m.tool("append_content", {"note_id": note_a, "content":"<p>line 2</p>"})
        ok(f"appended, bytes_written={ap['bytes_written']}")
        g4 = m.tool("get_note", {"note_id": note_a, "include_content": True})
        assert "completely new body" in g4["content"]
        assert "line 2" in g4["content"]
        ok("verified: both old and appended lines present")

        section("append_content with custom separator")
        m.tool("append_content", {"note_id": note_a, "content":"line 3", "separator": " | "})
        g5 = m.tool("get_note", {"note_id": note_a, "include_content": True})
        assert " | line 3" in g5["content"]
        ok("custom separator respected")

        section("remove_attribute")
        m.tool("remove_attribute", {"attribute_id": lbl_id})
        ok(f"removed attribute {lbl_id}")
        attrs2 = m.tool("list_attributes", {"note_id": note_a})
        names_after = sorted([x["name"] for x in attrs2["attributes"]])
        assert "priority" not in names_after, f"priority still there: {names_after}"
        ok(f"priority label gone, remaining: {names_after}")

        section("error path: get_note on bogus id")
        try:
            m.tool("get_note", {"note_id":"NONEXISTENT_XYZ"})
            failures.append("error path: bogus id did not raise")
        except RuntimeError as e:
            assert "404" in str(e) or "not found" in str(e).lower()
            ok(f"correctly errored: {str(e)[:80]}...")

        section("delete_note (cleanup)")
        m.tool("delete_note", {"note_id": note_a})
        ok(f"deleted A {note_a}")
        m.tool("delete_note", {"note_id": note_b})
        ok(f"deleted B {note_b}")

        section("verify deletion")
        try:
            m.tool("get_note", {"note_id": note_a})
            failures.append("delete: get after delete still works")
        except RuntimeError as e:
            assert "404" in str(e)
            ok("post-delete get returned 404")

    except AssertionError as e:
        failures.append(f"assertion: {e}")
    except Exception as e:
        failures.append(f"exception: {e!r}")
    finally:
        m.close()

    print()
    if failures:
        print("FAIL")
        for f in failures: print(f"  - {f}")
        sys.exit(1)
    print(f"PASS — all tools exercised, all assertions held")

if __name__ == "__main__":
    main()
