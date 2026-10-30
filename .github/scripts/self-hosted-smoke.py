"""Exercise a fresh, disposable Compose deployment. Never use with production data."""

import concurrent.futures
import http.client
import http.cookiejar
import json
import pathlib
import stat
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


BASE = "http://localhost:8318"
API = "/v8/management"
jar = http.cookiejar.CookieJar()
browser = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def request(path, method="GET", body=None, headers=None, opener=None, want=200):
    merged = {"Origin": BASE, "Content-Type": "application/json", **(headers or {})}
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method, headers=merged)
    try:
        response = (opener or urllib.request.build_opener()).open(req)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        payload = response.read()
        assert response.status == want, f"{method} {path}: {response.status}, expected {want}"
        if "application/json" in response.headers.get("Content-Type", ""):
            return json.loads(payload)
        return payload.decode()


def login(password):
    result = request(API + "/auth/login", "POST", {"username": "admin", "password": password}, opener=browser)
    return {"X-CSRF-Token": result["csrf_token"]}


def compose(*args):
    subprocess.run(["docker", "compose", *args], check=True, stdout=subprocess.DEVNULL)


def ready():
    for _ in range(60):
        try:
            request("/healthz")
            return
        except (urllib.error.URLError, ConnectionError):
            time.sleep(1)
    raise AssertionError("service did not become ready")


def verify_proxy_login(password):
    with tempfile.TemporaryDirectory(prefix="cpa-login-proxy-") as directory:
        config = pathlib.Path(directory) / "Caddyfile"
        config.write_text("http://127.0.0.1:8319 {\n reverse_proxy 127.0.0.1:8318\n}\n")
        container = subprocess.check_output([
            "docker", "run", "--detach", "--rm", "--network", "host",
            "--volume", f"{config}:/etc/caddy/Caddyfile:ro", "caddy:2-alpine",
        ], text=True).strip()

        def attempt(source, password_value, forwarded="203.0.113.99"):
            connection = http.client.HTTPConnection("127.0.0.1", 8319, source_address=(source, 0))
            try:
                body = json.dumps({"username": "admin", "password": password_value})
                connection.request("POST", API + "/auth/login", body, {
                    "Origin": BASE, "Content-Type": "application/json",
                    "X-Forwarded-For": forwarded, "X-Real-IP": forwarded,
                })
                response = connection.getresponse()
                response.read()
                return response.status
            finally:
                connection.close()

        try:
            for _ in range(60):
                try:
                    urllib.request.urlopen("http://127.0.0.1:8319/healthz").close()
                    break
                except (urllib.error.URLError, ConnectionError):
                    time.sleep(1)
            else:
                raise AssertionError("test reverse proxy did not become ready")
            for i in range(5):
                assert attempt("127.0.0.2", "wrong-password", f"198.51.100.{i + 1}") == 401
            assert attempt("127.0.0.2", password, "127.0.0.3") == 429, "forged proxy headers bypassed throttling"
            assert attempt("127.0.0.3", password, "127.0.0.2") == 200, "another client was locked out"
        finally:
            subprocess.run(["docker", "rm", "--force", container], check=True, stdout=subprocess.DEVNULL)


ready()
credentials = pathlib.Path("data/admin/initial-credentials.txt")
assert stat.S_IMODE(credentials.stat().st_mode) == 0o600
values = dict(line.split(": ", 1) for line in credentials.read_text().splitlines())
password = values["password"]
assert values["username"] == "admin" and len(password) >= 24
assert "CLIProxyAPI" in request("/admin/")
request("/v1/models", want=401)
csrf = login(password)
verify_proxy_login(password)
assert not credentials.exists(), "initial credentials were not removed on login"
request("/v1/models", opener=browser, want=401)
cookie = next(iter(jar))
assert cookie.path == API and cookie.has_nonstandard_attr("HttpOnly")
assert 29 * 86400 < cookie.expires - time.time() <= 30 * 86400
request(API + "/client-keys", "POST", {}, opener=browser, want=403)
first = request(API + "/client-keys", "POST", {}, csrf, browser, 201)
second = request(API + "/client-keys", "POST", {}, csrf, browser, 201)
first_header = {"Authorization": "Bearer " + first["key"]}
second_header = {"Authorization": "Bearer " + second["key"]}
request(API + "/client-keys", headers=first_header, want=403)
request("/v1beta/models", headers={"x-goog-api-key": first["key"]})
request("/v1/models", headers={"x-api-key": first["key"], "Anthropic-Version": "2023-06-01"})
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
    list(pool.map(lambda i: request("/v1/models", headers=first_header if i % 2 else second_header), range(16)))
request(API + "/auth/logout", "POST", {}, csrf, browser)
request("/v1/models", headers=first_header)
csrf = login(password)
replacement = password + "-new"
request(API + "/auth/password", "PUT", {"current_password": password, "new_password": replacement}, csrf, browser)
request("/v1/models", headers=first_header)
csrf = login(replacement)
compose("restart", "cpa")
ready()
request(API + "/auth/session", opener=browser, want=401)
request("/v1/models", headers=first_header)
assert not credentials.exists(), "restart regenerated administrator credentials"
csrf = login(replacement)
keys = request(API + "/client-keys", headers=csrf, opener=browser)["keys"]
assert {item["key"] for item in keys} == {first["key"], second["key"]}
request(API + "/client-keys/" + first["id"], "DELETE", headers=csrf, opener=browser)
request("/v1/models", headers=first_header, want=401)
request("/v1/models", headers=second_header)
request(API + "/client-keys/" + second["id"], "DELETE", headers=csrf, opener=browser)
request("/v1/models", headers=second_header, want=401)
request("/v1/models", want=401)
compose("restart", "cpa")
ready()
request("/v1/models", headers=first_header, want=401)
print("PASS: initialization, proxy login isolation, forged headers, CSRF, protocol keys, concurrency, logout, password change, restart, revocation")
