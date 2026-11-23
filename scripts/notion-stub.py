#!/usr/bin/env python3
"""A stand-in Notion server for scripts/e2e.sh, standard library only.

It implements the part of the 2025-09-03 API that Foreman's Notion backend uses,
the same part the Go fake in internal/notion/fake_test.go serves. It also serves a
few /__admin routes that stand in for a person editing pages in Notion:

  POST /__admin/create   {"set": {"Name": "x"}}                 -> {"id": ...}
  POST /__admin/edit     {"find": {"Name": "x"}, "set": {...}}  -> {"id": ...}
  GET  /__admin/page?prop=Name&equals=x                         -> flat page JSON
  GET  /__admin/pages                                           -> all live task pages
  POST /__admin/fail     {"method":"PATCH","prefix":"/v1/blocks","status":400}
                         answers matching requests with status until {"clear":true}

"set" values are plain JSON; the property's type decides how they are stored.

  scripts/notion-stub.py PORT_FILE PARENT_FILE [TOKEN]

It binds a free port, writes it to PORT_FILE, writes the id of a blank parent page
to PARENT_FILE, and serves until it is killed.
"""
import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

LOCK = threading.Lock()
TOKEN = "secret_stub_token"
SEQ = [0]
LAST = [0.0]
DBS, SOURCES, PAGES, BLOCKS, ORDER = {}, {}, {}, {}, {}
PAGE_SIZE = 100
FAIL = {}


def new_id(prefix):
    SEQ[0] += 1
    return prefix + "%031x" % SEQ[0]


def tick():
    """Strictly increasing time, so an edit is always later than the one before."""
    LAST[0] = max(time.time(), LAST[0] + 1)
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(LAST[0]))


def plain(v):
    if isinstance(v, dict):
        for k in ("rich_text", "title"):
            if k in v:
                v = v[k]
                break
    if not isinstance(v, list):
        return ""
    return "".join(rt["text"]["content"] for rt in v)


def page_json(pg):
    src = SOURCES.get(pg["source"])
    props = {}
    for name, v in pg["props"].items():
        typ = src["props"].get(name, "") if src else ""
        entry = {"type": typ}
        if typ in ("title", "rich_text"):
            rts = v if isinstance(v, list) else v.get(typ, [])
            for rt in rts:
                rt["plain_text"] = rt["text"]["content"]
            entry[typ] = rts
        else:
            entry[typ] = v.get(typ) if isinstance(v, dict) else v
        props[name] = entry
    return {"object": "page", "id": pg["id"], "in_trash": pg["trashed"], "created_time": pg["created"],
            "last_edited_time": pg["edited"], "properties": props}


def block_json(b):
    return {"object": "block", "id": b["id"], "type": b["typ"], b["typ"]: b["content"], "has_children": bool(b["children"])}


def add_blocks(parent, kids):
    out = []
    for k in kids:
        typ = k["type"]
        content = dict(k.get(typ) or {})
        nested = content.pop("children", [])
        for rt in content.get("rich_text", []) or []:
            rt["plain_text"] = rt["text"]["content"]
        b = {"id": new_id("b"), "parent": parent, "typ": typ, "content": content, "children": [], "deleted": False}
        BLOCKS[b["id"]] = b
        if parent in BLOCKS:
            BLOCKS[parent]["children"].append(b["id"])
        elif parent in PAGES:
            ORDER.setdefault(parent, []).append(b["id"])
            PAGES[parent]["edited"] = tick()
        if nested:
            add_blocks(b["id"], nested)
        out.append(block_json(b))
    return out


def child_ids(parent):
    if parent in BLOCKS:
        return BLOCKS[parent]["children"]
    return ORDER.get(parent, [])


def make_page(source, props):
    now = tick()
    pg = {"id": new_id("p"), "source": source, "props": dict(props), "created": now, "edited": now, "trashed": False}
    PAGES[pg["id"]] = pg
    return pg


def encode(typ, v):
    if typ in ("title", "rich_text"):
        return {typ: [{"text": {"content": v}}]}
    if typ == "select":
        return {typ: {"name": v}}
    if typ == "multi_select":
        return {typ: [{"name": x} for x in v]}
    if typ == "date":
        return {typ: {"start": v}}
    return {typ: v}


def tasks_source():
    for db in DBS.values():
        if db["title"] == "Tasks":
            return SOURCES[db["source"]]
    return None


def find_page(find):
    src = tasks_source()
    for pg in sorted(PAGES.values(), key=lambda p: p["created"]):
        if src and pg["source"] == src["id"] and not pg["trashed"] and all(plain(pg["props"].get(k)) == v for k, v in find.items()):
            return pg
    return None


def flat(pg):
    out = {"id": pg["id"]}
    for name, v in page_json(pg)["properties"].items():
        val = v.get(v["type"])
        if v["type"] in ("title", "rich_text"):
            val = "".join(rt["plain_text"] for rt in val)
        elif v["type"] == "select":
            val = val and val["name"]
        out[name] = val
    return out


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def reply(self, status, obj):
        raw = json.dumps(obj).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def not_found(self):
        self.reply(404, {"code": "object_not_found", "message": "Could not find the object."})

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        if not n:
            return {}
        return json.loads(self.rfile.read(n))

    def do_GET(self):
        self.handle_request()

    def do_POST(self):
        self.handle_request()

    def do_PATCH(self):
        self.handle_request()

    def do_DELETE(self):
        self.handle_request()

    def handle_request(self):
        with LOCK:
            body = self.body()
            u = urlparse(self.path)
            parts = u.path.strip("/").split("/")
            if parts[0] == "__admin":
                return self.admin(parts[1], u, body)
            if FAIL and self.command == FAIL["method"] and u.path.startswith(FAIL["prefix"]):
                return self.reply(FAIL["status"], {"code": "injected", "message": "injected failure"})
            if self.headers.get("Authorization") != "Bearer " + TOKEN:
                return self.reply(401, {"code": "unauthorized", "message": "API token is invalid."})
            if parts[0] != "v1" or len(parts) < 2:
                return self.reply(404, {"code": "not_found", "message": "no route"})
            self.route(self.command, parts[1:], u, body)

    def route(self, method, route, u, body):
        r0, n = route[0], len(route)
        if method == "GET" and r0 == "users" and n == 2:
            return self.reply(200, {"object": "user", "id": "bot", "type": "bot"})
        if method == "POST" and r0 == "databases" and n == 1:
            return self.create_db(body)
        if method == "GET" and r0 == "databases" and n == 2:
            db = DBS.get(route[1])
            if not db:
                return self.not_found()
            return self.reply(200, {"object": "database", "id": db["id"], "data_sources": [{"id": db["source"], "name": db["title"]}]})
        if r0 == "data_sources" and n == 2:
            src = SOURCES.get(route[1])
            if not src:
                return self.not_found()
            if method == "PATCH":
                for name, spec in (body.get("properties") or {}).items():
                    if spec:
                        for typ in spec:
                            src["props"][name] = typ
            return self.reply(200, self.source_json(src))
        if method == "POST" and r0 == "data_sources" and n == 3 and route[2] == "query":
            return self.query(route[1], body)
        if method == "POST" and r0 == "pages" and n == 1:
            parent = body["parent"]
            source = parent.get("data_source_id", "")
            if source and source not in SOURCES:
                return self.not_found()
            pg = make_page(source, body.get("properties") or {})
            if body.get("children"):
                add_blocks(pg["id"], body["children"])
            return self.reply(200, page_json(pg))
        if r0 == "pages" and n == 2:
            pg = PAGES.get(route[1])
            if not pg:
                return self.not_found()
            if method == "PATCH":
                pg["props"].update(body.get("properties") or {})
                if "in_trash" in body:
                    pg["trashed"] = bool(body["in_trash"])
                pg["edited"] = tick()
            return self.reply(200, page_json(pg))
        if r0 == "blocks" and n == 3 and route[2] == "children":
            return self.children(method, u, route[1], body)
        if method == "DELETE" and r0 == "blocks" and n == 2:
            b = BLOCKS.get(route[1])
            if not b:
                return self.not_found()
            b["deleted"] = True
            return self.reply(200, {"id": b["id"], "archived": True})
        self.reply(404, {"code": "not_found", "message": "no route " + u.path})

    def source_json(self, s):
        return {"object": "data_source", "id": s["id"], "properties": {n: {"name": n, "type": t} for n, t in s["props"].items()}}

    def create_db(self, body):
        title = body["title"][0]["text"]["content"]
        db = {"id": new_id("d"), "title": title, "parent": body["parent"]["page_id"]}
        src = {"id": new_id("s"), "props": {}}
        for name, spec in ((body.get("initial_data_source") or {}).get("properties") or {}).items():
            for typ in spec:
                src["props"][name] = typ
        db["source"] = src["id"]
        DBS[db["id"]], SOURCES[src["id"]] = db, src
        self.reply(200, {"object": "database", "id": db["id"], "data_sources": [{"id": src["id"], "name": title}]})

    def children(self, method, u, parent, body):
        if parent not in PAGES and parent not in BLOCKS:
            return self.not_found()
        if method == "PATCH":
            kids = body.get("children") or []
            if len(kids) > 100:
                return self.reply(400, {"code": "validation_error", "message": "children length should be <= 100"})
            return self.reply(200, {"object": "list", "results": add_blocks(parent, kids)})
        live = sorted("db:" + d["id"] for d in DBS.values() if d["parent"] == parent)
        live += [i for i in child_ids(parent) if not BLOCKS[i]["deleted"]]
        start = int((parse_qs(u.query).get("start_cursor") or ["0"])[0])
        end = min(start + PAGE_SIZE, len(live))
        results = []
        for i in live[start:end]:
            if i.startswith("db:"):
                d = DBS[i[3:]]
                results.append({"object": "block", "id": d["id"], "type": "child_database", "child_database": {"title": d["title"]}})
            else:
                results.append(block_json(BLOCKS[i]))
        resp = {"object": "list", "results": results, "has_more": end < len(live)}
        if end < len(live):
            resp["next_cursor"] = str(end)
        self.reply(200, resp)

    def query(self, source, body):
        if source not in SOURCES:
            return self.not_found()
        match = []
        for pg in PAGES.values():
            if pg["source"] != source or pg["trashed"]:
                continue
            flt = body.get("filter")
            if flt:
                cond = flt.get("rich_text") or flt.get("title")
                if plain(pg["props"].get(flt["property"])) != cond["equals"]:
                    continue
            match.append(pg)
        match.sort(key=lambda p: p["created"])
        start = int(body.get("start_cursor") or 0)
        end = min(start + PAGE_SIZE, len(match))
        resp = {"object": "list", "results": [page_json(p) for p in match[start:end]], "has_more": end < len(match)}
        if end < len(match):
            resp["next_cursor"] = str(end)
        self.reply(200, resp)

    def set_props(self, pg, values):
        src = SOURCES[pg["source"]]
        for name, v in values.items():
            typ = src["props"].get(name)
            if typ is None:
                return "no property " + name
            pg["props"][name] = encode(typ, v)
        pg["edited"] = tick()
        return None

    def admin(self, verb, u, body):
        if verb == "fail":
            FAIL.clear()
            if not body.get("clear"):
                FAIL.update(method=body["method"], prefix=body["prefix"], status=body.get("status", 400))
            return self.reply(200, {})
        src = tasks_source()
        if src is None:
            return self.reply(409, {"error": "Foreman has not created the Tasks database yet"})
        if verb == "create":
            pg = make_page(src["id"], {})
            err = self.set_props(pg, body.get("set") or {})
            return self.reply(400 if err else 200, {"error": err} if err else {"id": pg["id"]})
        if verb == "edit":
            pg = find_page(body.get("find") or {})
            if pg is None:
                return self.reply(404, {"error": "no such page"})
            err = self.set_props(pg, body.get("set") or {})
            return self.reply(400 if err else 200, {"error": err} if err else {"id": pg["id"]})
        if verb == "page":
            q = parse_qs(u.query)
            pg = find_page({q["prop"][0]: q["equals"][0]})
            return self.reply(200, flat(pg)) if pg else self.reply(404, {"error": "no such page"})
        if verb == "pages":
            live = [flat(p) for p in sorted(PAGES.values(), key=lambda p: p["created"]) if p["source"] == src["id"] and not p["trashed"]]
            return self.reply(200, live)
        self.reply(404, {"error": "no admin route"})


def main():
    global TOKEN
    port_file, parent_file = sys.argv[1], sys.argv[2]
    if len(sys.argv) > 3:
        TOKEN = sys.argv[3]
    parent = make_page("", {})
    del PAGES[parent["id"]]
    parent["id"] = new_id("a")  # config wants 32 hex characters
    PAGES[parent["id"]] = parent
    srv = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    with open(parent_file, "w") as f:
        f.write(parent["id"])
    with open(port_file, "w") as f:
        f.write(str(srv.server_address[1]))
    srv.serve_forever()


if __name__ == "__main__":
    main()
