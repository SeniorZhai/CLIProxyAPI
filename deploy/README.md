# Self-hosted administrator console

Deploy one CLIProxyAPI instance, connect upstream accounts in the browser, and create permanent device API keys. Projects and CLI execution stay on your devices. The deployment uses local files, with no database, Redis, billing, or user distribution service.

## Install on Linux

Install Docker Engine with the Compose plugin, then:

```sh
git clone --branch main https://github.com/SeniorZhai/CLIProxyAPI.git
cd CLIProxyAPI
cp deploy/.env.example deploy/.env
# Edit deploy/.env: set CPA_PUBLIC_URL=https://proxy.example.com
./deploy/install.sh
```

The image is built from this checkout, including the embedded console. Compose binds `127.0.0.1:8318` by default, separate from sub2. Change `CPA_PORT` if that port is already in use. The internal service port remains `8317`. No OAuth callback ports need to be published.

Point a separate domain at your server and add [Caddyfile.example](Caddyfile.example) to your existing Caddy configuration. Replace the domain and, if changed, the local port. Caddy handles HTTPS, WebSocket upgrades, and streaming responses. For an existing reverse proxy, forward the entire origin to the local port and preserve streaming and WebSocket upgrades. The example is a separate site definition and does not replace other services.

Compose uses a dedicated bridge (`172.30.83.0/24`, gateway `172.30.83.1`). If it overlaps another network, set both `CPA_NETWORK_SUBNET` and `CPA_NETWORK_GATEWAY` in `.env` before installation. First initialization sets `server.trusted-proxies` to that single gateway, so login throttling distinguishes clients behind the host reverse proxy. Keep the published port bound to loopback. The public reverse proxy must overwrite client-supplied forwarded IP headers; the example uses [Caddy's default header handling](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#headers). Do not trust all addresses or the whole Docker subnet. For another proxy topology, configure only its actual immediate peer address in `server.trusted-proxies` and restart.

Open `https://proxy.example.com/admin/`. Read the first login credentials on the server:

```sh
sudo cat deploy/data/admin/initial-credentials.txt
```

The default username is `admin`; a random password is generated. Only a bcrypt password hash is retained in `admin/account.json`. The initial credentials file has mode `0600` and is deleted on the first successful login. Optional `CPA_ADMIN_USERNAME` and `CPA_ADMIN_PASSWORD` apply only during first initialization. They do not rotate an existing account on restart.

For local testing, use `CPA_PUBLIC_URL=http://localhost:8318` and open that exact address. `public-url` must be an origin without a path. For an existing deployment, explicitly add `management.admin` from `config.example.yaml`, enable it, set `management.allow-remote: true` when needed, and restart. Automatic setup never overwrites an existing configuration. This mode requires standalone file storage; Home and alternate storage backends are not supported.

## Connect accounts and devices

1. Choose **Add account** in the console. Antigravity, ChatGPT Codex, Claude Code, Grok Build, Muse Code, and Devin are supported, alongside Kimi. Authorize on the provider's official page. Device flows show a code; redirect flows offer a field for pasting the full callback URL if the browser returns to an unreachable localhost address. Existing JSON credentials can also be imported.
2. Generate a **Device Key**, one per device if desired. All keys access the same configured upstream accounts. Empty or invalid key lists deny inference requests, including WebSocket connections.
3. Open **Client connection**, choose a key and an available model, then copy a Codex CLI, OpenAI, Claude Code, or Gemini example. OpenAI-compatible clients use `https://proxy.example.com/v1`. Claude uses the origin; Gemini uses `/v1beta`. Models depend on the connected accounts.

Browser sessions last **30 days**. Logout, password changes, or a server restart require signing in again. Device keys have **no expiry** and survive those events, as well as upgrades. Only explicit key removal/revocation invalidates them. Revocation rejects subsequent authenticated requests; an already running request or WebSocket is not forcibly terminated.

Administrator cookies are HttpOnly, scoped to `/v8/management`, and Secure for HTTPS deployments. State-changing management requests require the configured Origin and the session's CSRF token. Device keys cannot sign into the console; administrator cookies cannot authorize model calls. The old management-key mode remains available independently when configured.

## Persistence, upgrades, and recovery

`deploy/data/` contains the configuration (including device keys), upstream credentials, administrator hash, and logs. Back up the entire directory with restricted access. Keep it across container replacement. Use a single service replica for this file-based deployment.

When upgrading an earlier self-hosted deployment that did not configure trusted proxies, first run `docker compose -f deploy/compose.yaml down` (without `-v`) to replace its old bridge network. Set `server.trusted-proxies` in `deploy/data/config.yaml` to `["172.30.83.1"]`, or your chosen `CPA_NETWORK_GATEWAY`, before running the upgrade commands below. Existing data stays in `deploy/data/`. Without this migration, all clients behind the proxy still share the same login throttle.

```sh
git pull --ff-only
./deploy/install.sh
```

The install script preserves `.env` and existing data. Initialization never regenerates a saved administrator or device key. After initial setup, change the public origin in `deploy/data/config.yaml` (`management.admin.public-url`) and restart the service; editing the bootstrap environment alone does not rewrite it.

`CPA_TRUSTED_PROXIES` is a comma-separated bootstrap input and applies only when creating the configuration; it never overrides a saved configuration.

If the administrator password is lost, reset it locally while the service is stopped:

```sh
cd deploy
docker compose stop cpa
docker compose run --rm --no-deps cpa /CLIProxyAPI/CLIProxyAPI --config /data/config.yaml --reset-admin-password
sudo cat data/admin/initial-credentials.txt
docker compose up -d cpa
```

The reset generates a new password and preserves device keys and upstream credentials. If initialization was interrupted after saving the hash but before writing the initial credentials file, use the same reset command. Corrupt administrator state causes startup to fail instead of silently creating a replacement account.

## Verification

Run `go test ./...` and `go build -o test-output ./cmd/server`. The `self-hosted` GitHub Actions workflow builds the Linux container and checks automatic initialization, authentication isolation, key creation, logout, restart persistence, and revocation using a disposable Compose data directory. It also runs a Caddy reverse proxy to verify per-client login throttling and rejection of forged forwarded IP headers. Real provider subscriptions and live model calls require separately configured accounts.
