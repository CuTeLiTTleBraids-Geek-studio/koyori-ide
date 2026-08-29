# Authenticated server image

The server image places the Wails process on container loopback and exposes
only an authentication gateway on port 8080. The gateway refuses to start
unless exactly one of these is configured:

- `KOYORI_SERVER_TOKEN`: a random token containing at least 32 bytes.
- `KOYORI_SERVER_TOKEN_FILE`: a file whose trimmed contents contain the token.

The browser presents a local sign-in form and exchanges the token for an
HttpOnly, SameSite=Strict session cookie. API clients may send
`Authorization: Bearer <token>`. The gateway also authenticates the Wails RPC
and event WebSocket paths, rejects cross-origin browser requests, strips its
credentials before proxying, accepts RPC only over POST, and limits request
bodies to 32 MiB by default. Cookie-authenticated RPC also requires an explicit
same-origin `Origin` header; API clients without an Origin must use Bearer auth.

## Local use

PowerShell:

```powershell
$bytes = New-Object byte[] 32
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($bytes)
$rng.Dispose()
$env:KOYORI_SERVER_TOKEN = -join ($bytes | ForEach-Object { $_.ToString('x2') })
task run:docker
```

POSIX shell:

```sh
export KOYORI_SERVER_TOKEN="$(openssl rand -hex 32)"
task run:docker
```

`task run:docker` publishes `127.0.0.1:8080` by default. Open
`http://127.0.0.1:8080` and enter the token. `PORT` changes the host port.

## Remote deployment

Remote exposure is an explicit opt-in. For a trusted HTTPS reverse proxy, set
the public origin and bind the Docker port deliberately:

```sh
KOYORI_SERVER_TOKEN="$(openssl rand -hex 32)" \
KOYORI_EXTERNAL_ORIGIN=https://ide.example.com \
task run:docker HOST_IP=0.0.0.0 PORT=8080
```

Do not send the token over plain HTTP on an untrusted network. Either terminate
HTTPS at a trusted reverse proxy (and set `KOYORI_EXTERNAL_ORIGIN` to its
public origin) or mount a certificate and key and set
`KOYORI_TLS_CERT_FILE` and `KOYORI_TLS_KEY_FILE`.

For orchestrated deployments, prefer a read-only secret mount over an
environment variable:

```sh
docker run --rm \
  --mount type=bind,src=/absolute/path/koyori-token,dst=/run/secrets/koyori-token,readonly \
  -e KOYORI_SERVER_TOKEN_FILE=/run/secrets/koyori-token \
  -e KOYORI_EXTERNAL_ORIGIN=https://ide.example.com \
  -p 127.0.0.1:8080:8080 \
  koyori-ide:latest
```

Additional settings:

| Variable | Default | Purpose |
| --- | --- | --- |
| `KOYORI_GATEWAY_HOST` | `0.0.0.0` | Gateway bind address inside the container |
| `KOYORI_GATEWAY_PORT` | `8080` | Gateway port inside the container |
| `KOYORI_INTERNAL_PORT` | `8081` | Loopback-only Wails port |
| `KOYORI_MAX_REQUEST_BYTES` | `33554432` | Maximum authenticated request body |
| `KOYORI_TLS_CERT_FILE` | unset | PEM certificate used by the gateway |
| `KOYORI_TLS_KEY_FILE` | unset | PEM private key used by the gateway |
| `KOYORI_EXTERNAL_ORIGIN` | unset | Public HTTP(S) origin; required for non-loopback hosts |

The unauthenticated `/health` endpoint accepts only GET and HEAD and reports the
internal server's health. All application, RPC, and WebSocket routes require
authentication.

The gateway launches the private Wails process with a fresh per-start nonce.
Only requests proxied by that gateway carry the nonce; the standalone server
transport rejects a client-controlled mode flag and direct loopback RPC calls.
