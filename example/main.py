#!/usr/bin/env python3
import os, sqlite3, json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from socketserver import ThreadingMixIn

DB_FILE = "db.sqlite3"

def init_db():
    if os.path.exists(DB_FILE):
        os.remove(DB_FILE)
    conn = sqlite3.connect(DB_FILE, isolation_level=None)
    cur = conn.cursor()
    cur.execute("CREATE TABLE users (username TEXT PRIMARY KEY, money INTEGER)")
    cur.executemany("INSERT INTO users VALUES (?, ?)", [("alice", 100), ("bob", 100)])
    conn.close()

def log(msg):
    print(msg, file=sys.stderr, flush=True)

def log_balances():
    conn = sqlite3.connect(DB_FILE)
    cur = conn.cursor()
    cur.execute("SELECT username, money FROM users ORDER BY username")
    rows = cur.fetchall()
    balances = ", ".join(f"{u}={m}" for u, m in rows)
    log(f"Balances: {balances}")
    conn.close()

def with_balance_logging(func):
    def wrapper(self, *args, **kwargs):
        try:
            return func(self, *args, **kwargs)
        finally:
            log_balances()
    return wrapper

class Handler(BaseHTTPRequestHandler):
    @with_balance_logging
    def do_POST(self):
        log(f"Incoming request: {self.command} {self.path}")
        parts = self.path.strip("/").split("/")
        if len(parts) != 3 or parts[-1] != "transfer":
            self.send_response(404)
            self.end_headers()
            return

        mode = parts[0]       # "secure" or "insecure"
        user = parts[1]

        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        try:
            data = json.loads(body.decode())
            target = data["target_user"]
            amount = int(data["amount"])
        except Exception:
            self.send_response(400)
            self.end_headers()
            return

        if mode == "insecure":
            conn = sqlite3.connect(DB_FILE)
            cur = conn.cursor()
            try:
                cur.execute("SELECT money FROM users WHERE username=?", (user,))
                row = cur.fetchone()
                if not row or row[0] < amount:
                    conn.rollback()
                    conn.close()
                    self.send_response(400)
                    self.end_headers()
                    return
                cur.execute("UPDATE users SET money = money - ? WHERE username=?", (amount, user))
                cur.execute("UPDATE users SET money = money + ? WHERE username=?", (amount, target))
                conn.commit()
            except Exception:
                conn.rollback()
                conn.close()
                self.send_response(500)
                self.end_headers()
                return
            conn.close()

        elif mode == "secure":
            conn = sqlite3.connect(DB_FILE)
            cur = conn.cursor()
            try:
                cur.execute("BEGIN IMMEDIATE")
                # debit if enough balance
                cur.execute(
                    "UPDATE users SET money = money - ? WHERE username=? AND money >= ?",
                    (amount, user, amount),
                )
                if cur.rowcount != 1:
                    conn.execute("ROLLBACK")
                    conn.close()
                    self.send_response(400)
                    self.end_headers()
                    return
                # credit in same tx
                cur.execute("UPDATE users SET money = money + ? WHERE username=?", (amount, target))
                conn.execute("COMMIT")
            except Exception:
                try:
                    conn.execute("ROLLBACK")
                except Exception:
                    pass
                conn.close()
                self.send_response(500)
                self.end_headers()
                return
            conn.close()
        else:
            self.send_response(404)
            self.end_headers()
            return

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        resp = {"from": user, "to": target, "amount": amount, "mode": mode}
        try:
            self.wfile.write(json.dumps(resp).encode())
        except BrokenPipeError:
            log("Client closed connection before response finished")

class ThreadingHTTPServer(ThreadingMixIn, HTTPServer):
    daemon_threads = True

def run():
    init_db()
    server = ThreadingHTTPServer(("0.0.0.0", 8000), Handler)
    log("Server running on http://0.0.0.0:8000")
    server.serve_forever()

if __name__ == "__main__":
    run()
