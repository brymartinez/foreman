#!/usr/bin/env python3
"""Local GraphQL stand-in for the mixed-source end-to-end scenario."""
import http.server
import json
import os
import sys

state = sys.argv[1]
project_url = "https://linear.app/acme/project/linear-work-123"
issue_id = "11111111-1111-4111-8111-111111111111"
issue_url = "https://linear.app/acme/issue/LIN-7/add-search"


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        query = request["query"]
        variables = request.get("variables") or {}
        with open(os.path.join(state, "linear.log"), "a") as log:
            log.write(query.split("{")[0].strip() + "\n")
        if os.path.exists(os.path.join(state, "linear-fail")):
            self.send_response(503)
            self.end_headers()
            return
        if "viewer { id }" in query:
            data = {"viewer": {"id": "viewer-1"}}
        elif "projects(first:" in query:
            data = {"projects": {"nodes": [{"id": "project-1", "url": project_url}], "pageInfo": {"hasNextPage": False}}}
        elif "issues(first:" in query:
            data = {"issues": {"nodes": [self.issue(True)], "pageInfo": {"hasNextPage": False}}}
        elif "comments(first:" in query:
            data = {"issue": {"comments": {"nodes": [], "pageInfo": {"hasNextPage": False}}}}
        elif "issue(id:" in query:
            data = {"issue": self.issue(False) if variables.get("id") == issue_id else None}
        elif "commentCreate(" in query:
            number = len([name for name in os.listdir(state) if name.startswith("linear-comment-")]) + 1
            body = variables["body"]
            with open(os.path.join(state, f"linear-comment-{number}"), "w") as comment:
                comment.write(body)
            data = {"commentCreate": {"success": True, "comment": {"id": f"comment-{number}", "body": body, "url": issue_url + f"#comment-{number}"}}}
        elif "comment(id:" in query:
            data = {"comment": {"id": variables["id"], "body": "", "url": issue_url}}
        else:
            data = {"commentUpdate": {"success": True, "comment": {"id": variables.get("id", ""), "body": variables.get("body", ""), "url": issue_url}}}
        body = json.dumps({"data": data}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def issue(self, discovery):
        issue = {"id": issue_id, "identifier": "LIN-7", "branchName": "bryan/lin-7-add-search", "title": "Add search", "description": "Add search to the widget list", "url": issue_url, "updatedAt": "2026-10-01T10:00:00Z", "state": {"type": "unstarted"}, "labels": {"nodes": [{"name": "triaged"}]}}
        if discovery:
            issue.update(createdAt="2026-09-25T10:00:00Z", assignee={"id": "viewer-1"}, project={"id": "project-1"})
        return issue

    def log_message(self, *args):
        pass


server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
with open(os.path.join(state, "linear-port"), "w") as port:
    port.write(str(server.server_port))
server.serve_forever()
