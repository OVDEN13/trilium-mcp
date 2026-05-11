# end-to-end test

`test_e2e.py` drives the `trilium-mcp` binary over stdio and exercises all ten tools against a live TriliumNext instance.

## Run

```bash
# from the repo root, after building the binary:
TRILIUM_URL=http://localhost:8092 TRILIUM_TOKEN=your-etapi-token \
  python3 e2e/test_e2e.py
```

The script:

1. Sends `initialize` + verifies `tools/list` reports exactly the expected ten tools.
2. Creates two notes (one with inline labels, one as a relation target).
3. Adds a label and a relation, checks `list_attributes` returns all four.
4. Searches by label, with and without ancestor scoping.
5. Gets a note with and without `include_content`.
6. Updates a note's title (only), then replaces its content, then appends to it (default + custom separator).
7. Removes one label, re-lists attributes to confirm it's gone.
8. Hits `get_note` on a bogus id to verify error semantics (`isError:true`, HTTP 404 surfaced).
9. Deletes both notes and confirms post-delete `get_note` returns 404.

It exits non-zero on the first failed assertion. The notes it creates are deleted at the end (or left behind only if an earlier step crashed).

## Why not in CI

Running this in CI would need to bootstrap a real TriliumNext (set up account, create an ETAPI token) inside the workflow — currently more friction than value. The CI workflow only runs the stdio handshake smoke test. If you want the full e2e in CI, see [issues](https://github.com/OVDEN13/trilium-mcp/issues).
