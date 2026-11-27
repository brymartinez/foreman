#!/usr/bin/env python3
"""Read back the Foreman probe and supply Notion edits for its opt-in workflow."""
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

API = "https://api.notion.com"
FIELDS = {
    "Name": "title",
    "Repository": "rich_text",
    "Confirmed triage": "rich_text",
    "Scope confirmed": "checkbox",
    "Queue": "checkbox",
    "Answer": "rich_text",
}
TASK_NAME = "Search page"


def request(path, method="GET", data=None):
    headers = {
        "Authorization": "Bearer " + os.environ["FOREMAN_NOTION_TOKEN"],
        "Notion-Version": os.environ["FOREMAN_PROBE_NOTION_VERSION"],
        "Content-Type": "application/json",
    }
    payload = json.dumps(data).encode() if data is not None else None
    query = urllib.request.Request(API + path, data=payload, headers=headers, method=method)
    for attempt in range(4):
        try:
            with urllib.request.urlopen(query, timeout=15) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            if error.code != 429 or attempt == 3:
                raise ValueError(f"Notion returned HTTP {error.code} for {method} {path}") from None
            time.sleep(min(int(error.headers.get("Retry-After", "1")), 60))


def blocks(parent):
    out = []
    cursor = None
    while True:
        path = f"/v1/blocks/{parent}/children"
        if cursor:
            path += "?" + urllib.parse.urlencode({"start_cursor": cursor})
        page = request(path)
        out.extend(page["results"])
        cursor = page.get("next_cursor")
        if not cursor:
            return out


def task_source(parent):
    matches = [block["id"] for block in blocks(parent)
               if block["type"] == "child_database" and block["child_database"]["title"] == "Tasks"]
    if len(matches) != 1:
        raise ValueError(f"expected one Tasks database, found {len(matches)}")
    sources = request(f"/v1/databases/{matches[0]}")["data_sources"]
    if len(sources) != 1:
        raise ValueError(f"expected one Tasks data source, found {len(sources)}")
    return sources[0]["id"]


def task(source, prop, kind, value):
    result = request(f"/v1/data_sources/{source}/query", "POST",
                     {"filter": {"property": prop, kind: {"equals": value}}})
    pages = [p for p in result["results"] if not p.get("in_trash")]
    if len(pages) != 1 or result.get("has_more"):
        raise ValueError(f"expected one page with {prop} {value!r}, found {len(pages)}")
    return pages[0]


def flatten(page):
    values = {}
    for name, prop in page["properties"].items():
        kind = prop["type"]
        value = prop.get(kind)
        if kind in {"title", "rich_text"}:
            value = "".join(item["plain_text"] for item in (value or []))
        values[name] = value if value is not None else ""
    return values


def properties(values):
    props = {}
    for name, value in values.items():
        kind = FIELDS[name]
        if kind in {"rich_text", "title"}:
            value = [{"type": "text", "text": {"content": value}}]
        props[name] = {kind: value}
    return props


def all_blocks(parent):
    children = blocks(parent)
    for block in children:
        if block.get("has_children"):
            block["children"] = all_blocks(block["id"])
    return children


def find_page_id(raw):
    segment = urllib.parse.urlsplit(raw.strip()).path.rstrip("/").rsplit("/", 1)[-1]
    match = re.search(r"[0-9a-fA-F]{32}$", segment.replace("-", ""))
    if not match:
        raise ValueError("expected a Notion parent page ID or URL")
    return match[0].lower()


def check_task(source, issue_id, title, scope=None):
    page = task(source, "Foreman ID", "rich_text", issue_id)
    values = flatten(page)
    if values.get("Name") != title or (values.get("Status") or {}).get("name") != "Inbox":
        raise ValueError(f"Notion task {issue_id} did not publish its title and status")
    def text(block):
        return "".join(item["plain_text"] for item in block[block["type"]]["rich_text"])

    record_link = "https://github.com/brymartinez/foreman#notion-record-" + page["id"].replace("-", "")
    page_blocks = all_blocks(page["id"])
    records = []
    for block in page_blocks:
        if block["type"] != "heading_2" or text(block) != "Foreman record":
            continue
        label = block["heading_2"]["rich_text"]
        if len(label) != 1 or (label[0].get("text", {}).get("link") or {}).get("url") != record_link:
            continue
        children = block.get("children", [])
        if children and children[-1]["type"] == "paragraph" and text(children[-1]) == "End of Foreman record":
            records.append("".join(text(child) for child in children if child["type"] == "code"))

    description = "Created by scripts/probe-notion.sh."
    expected = {f"id: {issue_id}", "status: INBOX", f"  description: {description}"}
    if scope is not None:
        expected.add(f"triage: {scope}")
    if not records or not expected.issubset(records[-1].splitlines()):
        raise ValueError(f"Notion task {issue_id} did not publish the expected complete record")
    if not any(block["type"] == "paragraph" and text(block) == description for block in page_blocks):
        raise ValueError(f"Notion task {issue_id} did not publish its description")
    if scope is not None and (
            values.get("Confirmed triage") != scope or not values.get("Scope confirmed")):
        raise ValueError(f"Notion task {issue_id} did not publish its confirmed scope")


def main(verb, *args):
    if verb == "page-id":
        print(find_page_id(*args))
    elif verb == "blank":
        if blocks(*args):
            raise ValueError("the workflow probe requires an empty disposable parent page")
    elif verb == "source":
        print(task_source(*args))
    elif verb == "check":
        check_task(*args)
    elif verb in {"page", "dump"}:
        page = task(args[0], "Name", "title", TASK_NAME)
        values = flatten(page)
        if verb == "dump":
            values["blocks"] = all_blocks(page["id"])
        print(json.dumps(values))
    elif verb in {"create", "edit"}:
        source, raw = args
        props = properties(json.loads(raw)["set"])
        if verb == "create":
            request("/v1/pages", "POST", {"parent": {"type": "data_source_id", "data_source_id": source},
                                        "properties": props})
        else:
            page = task(source, "Name", "title", TASK_NAME)
            request(f"/v1/pages/{page['id']}", "PATCH", {"properties": props})
    else:
        raise ValueError(f"unknown probe operation {verb!r}")


if __name__ == "__main__":
    try:
        main(*sys.argv[1:])
    except (ValueError, KeyError, TypeError, urllib.error.URLError) as error:
        sys.exit(f"notion probe: {error}")
