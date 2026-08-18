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

Remote exposure is an explicit opt-in. The public HTTPS origin is mandatory so
the gateway can reject forged Host and Origin headers. For a trusted reverse
proxy deployment, for example:

```sh
export KOYORI_EXTERNAL_ORIGIN=https://ide.example.com
HOST_IP=127.0.0.1 task run:docker
```

Do not send the token over plain HTTP on an untrusted network. The task command
forwards `KOYORI_EXTERNAL_ORIGIN` and expects HTTPS termination at the proxy.
For direct TLS, mount the certificate and key read-only and use their container
paths explicitly:

```sh
export KOYORI_SERVER_TOKEN="$(openssl rand -hex 32)"
docker run --init --rm \
  -e KOYORI_SERVER_TOKEN \
  -e KOYORI_EXTERNAL_ORIGIN=https://ide.example.com:8443 \
  -e KOYORI_TLS_CERT_FILE=/run/tls/tls.crt \
  -e KOYORI_TLS_KEY_FILE=/run/tls/tls.key \
  --mount type=bind,src=/absolute/path/tls.crt,dst=/run/tls/tls.crt,readonly \
  --mount type=bind,src=/absolute/path/tls.key,dst=/run/tls/tls.key,readonly \
  -p 0.0.0.0:8443:8080 \
  koyori-ide:latest
```

For orchestrated deployments, prefer a read-only secret mount over an
environment variable:

```sh
docker run --rm \
  --mount type=bind,src=/absolute/path/koyori-token,dst=/run/secrets/koyori-token,readonly \
  -e KOYORI_SERVER_TOKEN_FILE=/run/secrets/koyori-token \
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
| `KOYORI_EXTERNAL_ORIGIN` | unset | Required public HTTPS origin for non-loopback Host/Origin validation |
| `KOYORI_TLS_CERT_FILE` | unset | PEM certificate used by the gateway |
| `KOYORI_TLS_KEY_FILE` | unset | PEM private key used by the gateway |

The unauthenticated `/health` endpoint accepts only GET and HEAD and reports the
internal server's health. All application, RPC, and WebSocket routes require
authentication.

The standalone `-tags server` binary applies the same network boundary in a
different way: it refuses a non-loopback `WAILS_SERVER_HOST`, defaults to
`127.0.0.1`, and uses the pinned Wails beta.8 same-origin WebSocket policy.
Do not weaken those guards to expose the raw Wails transport; use this
authenticated gateway for a remote deployment.

The gateway marks its private child process with an internal environment value
so the child does not re-apply the external Origin check after proxying. That
value is stripped from user input and is never a client authentication token.
