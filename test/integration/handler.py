#!/usr/bin/env python3
"""Local stand-in for the intercepted 'orders' service (runs on the CI host = the
"laptop"). Returns a LOCAL-LAPTOP marker so the harness can prove inbound takeover,
and GET /call-dep calls the in-cluster dependency to prove outbound via telepresence."""
import json
import os
import socket
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

DEP_URL = os.environ.get("DEP_URL", "")
PROFILE = os.environ.get("SPRING_PROFILES_ACTIVE", "<unset>")
PORT = int(os.environ.get("PORT", "8080"))


class Handler(BaseHTTPRequestHandler):
    def _send(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith("/call-dep"):
            try:
                with urllib.request.urlopen(DEP_URL, timeout=10) as r:
                    snippet = r.read().decode(errors="replace")
                self._send(200, {"servedBy": "LOCAL-LAPTOP orders", "dependencyReachable": True,
                                 "dependencyResponseSnippet": snippet[:300]})
            except Exception as e:  # noqa: BLE001
                self._send(502, {"servedBy": "LOCAL-LAPTOP orders", "dependencyReachable": False, "error": repr(e)})
            return
        self._send(200, {"servedBy": "LOCAL-LAPTOP orders", "hostname": socket.gethostname(),
                         "springProfilesActive": PROFILE, "depUrlFromClusterEnv": DEP_URL})

    def log_message(self, *args):  # quiet
        pass


if __name__ == "__main__":
    print(f"LOCAL orders on :{PORT} profile={PROFILE} DEP_URL={DEP_URL}", flush=True)
    HTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
