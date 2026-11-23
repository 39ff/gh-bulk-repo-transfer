# Fake GitHub API with sample data, used only to record the README animations.
import json, sys, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

ME = "alice"
accounts = {"alice": "User", "acme-inc": "Organization", "alice-archive": "Organization"}
repos = {}
def add(owner, name, pushed, desc="", private=False, fork=False, stars=0):
    repos[f"{owner}/{name}".lower()] = dict(
        name=name, full_name=f"{owner}/{name}", owner=dict(login=owner, type=accounts[owner]),
        private=private, visibility="private" if private else "public", fork=fork, archived=False,
        description=desc, pushed_at=pushed + "T09:00:00Z", stargazers_count=stars,
        html_url=f"https://github.com/{owner}/{name}", permissions=dict(admin=True))
add("alice", "jquery-playground", "2015-06-12", fork=True)
add("alice", "hackathon-2016", "2016-10-02", "Hack day project")
add("alice", "old-blog", "2017-03-21", "Jekyll blog")
add("alice", "todo-app-react", "2018-08-14", "Learning React")
add("alice", "ml-experiments", "2019-11-30", private=True)
add("alice", "coding-test-prep", "2020-04-05", private=True)
add("alice", "weather-bot", "2021-07-19", "Slack bot that posts the forecast", stars=3)
add("alice", "side-project-x", "2022-12-01")
add("alice", "dotfiles", "2026-10-28", "My dotfiles", stars=8)
add("alice", "portfolio", "2026-11-15", "Personal website", stars=24)
add("alice-archive", "old-blog", "2019-01-01")  # name conflict in the destination

class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def send(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
    def do_GET(self):
        u = urlparse(self.path); p = u.path.strip("/").split("/")
        time.sleep(0.05)
        if p == ["user"]: return self.send(200, dict(login=ME, type="User"))
        if p == ["user", "orgs"]: return self.send(200, [dict(login="acme-inc"), dict(login="alice-archive")])
        if p == ["user", "memberships", "orgs"]:
            return self.send(200, [dict(role="member", state="active", organization=dict(login="acme-inc")),
                                   dict(role="admin", state="active", organization=dict(login="alice-archive"))])
        if p == ["user", "repos"]:
            time.sleep(0.4)
            return self.send(200, sorted([r for r in repos.values() if r["owner"]["login"] == ME], key=lambda r: r["name"]))
        if len(p) == 2 and p[0] == "users" and p[1] in accounts: return self.send(200, dict(login=p[1], type=accounts[p[1]]))
        if len(p) == 3 and p[0] == "repos":
            r = repos.get(f"{p[1]}/{p[2]}".lower())
            if r: return self.send(200, r)
        self.send(404, dict(message="Not Found"))
    def do_POST(self):
        p = self.path.strip("/").split("/")
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        time.sleep(0.35)
        r = repos.pop(f"{p[1]}/{p[2]}".lower())
        r["owner"] = dict(login=body["new_owner"], type="Organization")
        r["full_name"] = body["new_owner"] + "/" + r["name"]
        repos[r["full_name"].lower()] = r
        self.send(202, r)
    def do_PATCH(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        time.sleep(0.15)
        p = self.path.strip("/").split("/")
        r = repos.get(f"{p[1]}/{p[2]}".lower(), {})
        r["archived"] = True
        self.send(200, r)

srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
print(srv.server_address[1], flush=True)
threading.Thread(target=srv.serve_forever, daemon=True).start()
sys.stdin.read()
