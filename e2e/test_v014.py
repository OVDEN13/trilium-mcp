#!/usr/bin/env python3
"""v0.1.4 e2e: move_note, clone_note, delete_branch, batch_*, get_note_subtree, HTML escape."""
import json, os, subprocess, sys, threading
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
        req = {"jsonrpc":"2.0","method":method,"params":params or {}}
        if want_response: req["id"]=self.nid
        self.p.stdin.write(json.dumps(req)+"\n"); self.p.stdin.flush()
        if not want_response: return None
        return self.q.get(timeout=20)
    def tool(self, name, args):
        r = self.call("tools/call", {"name": name, "arguments": args})
        if "error" in r: raise RuntimeError(f"{name} RPC error: {r['error']}")
        res = r["result"]
        if res.get("isError"):
            raise RuntimeError(f"{name} tool error: {res['content'][0]['text']}")
        return json.loads(res["content"][0]["text"])
    def raw_text(self, name, args):
        r = self.call("tools/call", {"name": name, "arguments": args})
        if "error" in r: raise RuntimeError(f"{name} RPC error: {r['error']}")
        res = r["result"]
        if res.get("isError"):
            raise RuntimeError(f"{name} tool error: {res['content'][0]['text']}")
        return res["content"][0]["text"]
    def close(self):
        self.p.stdin.close(); self.p.wait(timeout=5)

def section(t): print(f"\n=== {t} ===")
def ok(t): print(f"  ✓ {t}")

def main():
    m = Mcp()
    fails = []
    try:
        m.call("initialize",{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e-014","version":"0"}})
        m.call("notifications/initialized", want_response=False)
        tools = m.call("tools/list")["result"]["tools"]
        names = {t["name"] for t in tools}
        expected_new = {"move_note","clone_note","delete_branch","batch_create_notes","batch_delete_notes","get_note_subtree"}
        missing = expected_new - names
        if missing: fails.append(f"missing tools: {missing}")
        else: ok(f"all 6 new tools registered (total: {len(tools)})")

        section("HTML escape fix: get_note content should have raw < > &")
        a = m.tool("create_note",{"title":"v014 test A","content":"<p>angle brackets & ampersands</p>"})
        note_a = a["note_id"]
        ok(f"created A={note_a}")
        raw = m.raw_text("get_note",{"note_id":note_a,"include_content":True})
        if "\\u003c" in raw or "\\u0026" in raw:
            fails.append(f"raw JSON still escapes HTML: {raw[:200]}")
        else: ok("raw response uses < > & directly, no \\uXXXX escapes")

        section("batch_create_notes: 3 children under A")
        bc = m.tool("batch_create_notes", {"notes": [
            {"title":"batch child 1","parent_note_id":note_a,"labels":{"k":"v","n":42}},
            {"title":"batch child 2","parent_note_id":note_a,"content":"<p>two</p>"},
            {"title":"batch child 3","parent_note_id":note_a},
        ]})
        if bc["created"]!=3 or bc["failed"]!=0:
            fails.append(f"batch_create: created={bc['created']} failed={bc['failed']}")
        else: ok(f"3 created in one call")
        child_ids = [r["note_id"] for r in bc["results"]]
        # labels coerced from number
        c1_labels = m.tool("list_attributes",{"note_id":child_ids[0]})
        names_vals = {(a["name"], a["value"]) for a in c1_labels["attributes"]}
        if ("k","v") not in names_vals or ("n","42") not in names_vals:
            fails.append(f"batch label coercion broken: {names_vals}")
        else: ok("non-string label values coerced (42 → '42')")

        section("clone_note: attach child 1 under root as a second parent")
        cln = m.tool("clone_note",{"note_id":child_ids[0],"new_parent_id":"root","prefix":"see-also"})
        ok(f"second branch created: {cln['branchId']}")
        # idempotent — POST /branches re-issued returns 200 with same branch
        cln2 = m.tool("clone_note",{"note_id":child_ids[0],"new_parent_id":"root","prefix":"see-also"})
        if cln2["branchId"]!=cln["branchId"]:
            fails.append("clone not idempotent")
        else: ok("re-clone returns same branch_id (Trilium dedup)")
        # child 1 now has 2 parents (note_a and root)
        c1 = m.tool("get_note",{"note_id":child_ids[0]})["note"]
        if sorted(c1["parentNoteIds"]) != sorted([note_a,"root"]):
            fails.append(f"after clone, parents={c1['parentNoteIds']}, want both note_a and root")
        else: ok(f"child has 2 parents now: {c1['parentNoteIds']}")

        section("move_note: move child 2 from A to root")
        mv = m.tool("move_note",{"note_id":child_ids[1],"new_parent_id":"root"})
        ok(f"moved: new_parent={mv['new_parent_id']} removed_branch={mv['removed_branch']}")
        note_b = m.tool("get_note",{"note_id":child_ids[1]})["note"]
        if note_b["parentNoteIds"]!=["root"]:
            fails.append(f"after move, parents={note_b['parentNoteIds']}, want ['root']")
        else: ok("parent updated correctly")

        section("get_note_subtree: depth=2 from A")
        sub = m.tool("get_note_subtree",{"note_id":note_a,"max_depth":2})
        root = sub["root"]
        if root["note_id"]!=note_a:
            fails.append("subtree root mismatch")
        cnt_children = len(root.get("children",[]))
        if cnt_children < 2:
            fails.append(f"expected ≥2 children under A, got {cnt_children}")
        else: ok(f"subtree returned {sub['notes_visited']} notes, {cnt_children} direct children of A")

        section("delete_branch: un-clone child 1 (remove the root branch, keep under A)")
        m.tool("delete_branch",{"branch_id":cln["branchId"]})
        ok(f"deleted branch {cln['branchId']}")
        still = m.tool("get_note",{"note_id":child_ids[0]})["note"]
        if still["parentNoteIds"] != [note_a]:
            fails.append(f"after un-clone, parents={still['parentNoteIds']}, want [{note_a}]")
        else: ok("note kept original parent (root branch removed, note alive under A)")

        section("batch_delete_notes: clean up")
        # collect what we still have
        to_delete = [note_a, child_ids[0], child_ids[1], child_ids[2]]
        bd = m.tool("batch_delete_notes",{"note_ids": to_delete})
        ok(f"deleted {len(bd['deleted'])}, failed {len(bd['failed'])}")
        # verify A is gone
        try:
            m.tool("get_note",{"note_id":note_a})
            fails.append("A still exists after batch_delete")
        except RuntimeError as e:
            if "404" in str(e): ok("post-delete A returns 404")
            else: fails.append(f"unexpected error after delete: {e}")

    except Exception as e:
        fails.append(f"exception: {e!r}")
    finally:
        m.close()
    print()
    if fails:
        print("FAIL")
        for f in fails: print(f"  - {f}")
        sys.exit(1)
    print("PASS — all v0.1.4 features work")

if __name__ == "__main__":
    main()
