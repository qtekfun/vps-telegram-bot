# vps-telegram-bot

A small Telegram bot, written in Go with the standard library only, to check and
operate the Docker containers of a single personal VPS from a phone.

- `/status` - containers (state, health, restart count, uptime), host memory,
  load, uptime, disk usage and, optionally, the active alerts of a monitoring script.
- `/logs <container> [n]` - last `n` lines (default 30, max 200). Long output is
  sent as a `.txt` document.
- `/containers` - start, stop or restart containers from an allowlist, always
  behind a Yes/No confirmation that expires after 60 seconds.
- `/checks` - [Gatus](https://github.com/TwiN/gatus) endpoint status grouped by
  group, failing endpoints first.
- `/help`

It uses long polling (`getUpdates`): no webhook and no inbound port. It talks to
Docker only over TCP through a filtering proxy, never through a mounted
`docker.sock`.

## How it is protected

- Only the numeric Telegram user IDs in `ALLOWED_USER_IDS` get answers. Everyone
  else is ignored silently; their ID is written to the log (this is how you find
  your own ID). Messages from groups and channels are ignored too.
- Start/stop/restart exist only for the containers in `ACTION_CONTAINERS`. Names
  that reach Docker are always resolved against the live container list; user
  input is never put into an API path.
- Each action needs a confirmation. The confirmation is single use, bound to the
  user and chat that asked for it, and expires (`CONFIRM_TTL`, default 60 s).
- Runs as `nonroot` in a distroless static image, works with a read-only root
  filesystem, needs no shell and no capabilities.
- The bot token is read from a file (preferred) or the environment and is never
  logged; transport errors are stripped of the request URL, which contains it.

## BotFather setup

Open a chat with [@BotFather](https://t.me/BotFather) in Telegram.

1. `/newbot` - choose a display name (for example `My VPS`) and a username that
   ends in `bot` (for example `my_vps_ops_bot`). BotFather replies with the HTTP
   API token.
2. Store the token in a file readable only by the deploy user and never commit
   it, for example `/etc/vps-telegram-bot/token` with mode `0400`. Prefer
   `TELEGRAM_TOKEN_FILE` over `TELEGRAM_TOKEN`: environment variables show up in
   `docker inspect`.
3. `/setcommands` - pick the bot, then send exactly this list:

   ```
   status - Containers, host and alerts
   logs - Container logs: /logs name [lines]
   containers - Start, stop or restart a container
   checks - Gatus endpoint status
   help - Show help
   ```

   The bot also registers the same list itself on every start (`setMyCommands`).
4. `/setjoingroups` - pick the bot and choose `Disable`, so nobody can add it to
   a group.
5. `/setprivacy` - pick the bot and choose `Enable` (the default). The bot only
   works in private chats, so it never needs to read group traffic.
6. Optional: `/setdescription`, `/setabouttext` and `/setuserpic` for a
   description and avatar.

### Finding your user ID

Start the bot with a placeholder in `ALLOWED_USER_IDS` (it must contain at least
one valid number, for example `1`), send it any message, and read the log:

```
level=INFO msg="ignored update from unauthorized user" user_id=123456789
```

Put that number in `ALLOWED_USER_IDS` and restart. Alternatively ask a Telegram
bot such as `@userinfobot` for your ID.

## Configuration

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `TELEGRAM_TOKEN_FILE` | one of the two | | Path of a file holding the bot token (preferred). Wins over `TELEGRAM_TOKEN`. |
| `TELEGRAM_TOKEN` | one of the two | | The bot token itself. |
| `ALLOWED_USER_IDS` | yes | | Comma-separated numeric Telegram user IDs allowed to use the bot. |
| `DOCKER_HOST` | no | `tcp://socket-proxy:2375` | Docker API endpoint. Only `tcp://host:port` (a filtering proxy) is supported. |
| `ACTION_CONTAINERS` | no | empty | Comma-separated container names that may be started, stopped or restarted. Empty disables actions. |
| `GATUS_URL` | no | empty | Base URL of Gatus, for example `http://gatus:8080`. Empty disables `/checks`. |
| `STATUS_DISK_PATHS` | no | empty | Comma-separated paths whose filesystem usage `/status` reports (statfs), for example `/data`. Mount the host paths read-only into the container. |
| `MONITOR_STATE_PATH` | no | empty | Optional JSON state file, read-only: `{"active": {"key": "text"}}`. Active alerts are listed in `/status`. A missing file is skipped. |
| `CONFIRM_TTL` | no | `60s` | How long a Yes/No confirmation stays valid (`5s` to `10m`). |

Memory, load and uptime come from `/proc/meminfo`, `/proc/loadavg` and
`/proc/uptime`, which show host values even inside a container.

## Docker API calls and the socket proxy

The bot makes exactly these requests, so any allowlisting proxy can be
configured from this table:

| Method | Path | Used for |
| --- | --- | --- |
| `GET` | `/containers/json?all=1` | container list, name to ID resolution |
| `GET` | `/containers/{id}/json` | health, restart count, start time, TTY flag |
| `GET` | `/containers/{id}/logs?stdout=1&stderr=1&tail=N` | `/logs` |
| `POST` | `/containers/{id}/start` | start (after confirmation) |
| `POST` | `/containers/{id}/stop?t=10` | stop (after confirmation) |
| `POST` | `/containers/{id}/restart?t=10` | restart (after confirmation) |

`{id}` is always a hex container ID taken from the container list. It never
calls `exec`, `create`, `kill`, `rm`, `archive`, `export`, `/images`, `/volumes`
or anything else.

### Recommended: a small HAProxy allowlist

Generic proxies are too coarse for this. In `tecnativa/docker-socket-proxy`
(and the compatible `linuxserver/socket-proxy`) the `CONTAINERS` switch matches
the whole `/containers` prefix, so as soon as `POST=1` is set it also allows
`POST /containers/create` and `POST /containers/{id}/exec`, which is root on the
host. Both projects only offer per-action switches for start/stop/restart/kill,
not a way to keep `POST` away from the rest of `/containers`. Do not run them with
`POST=1` for this bot.

Instead run the official `haproxy` image with an explicit path allowlist
(`haproxy.cfg`, tested against a real Docker Engine):

```
global
    log stdout format raw local0

defaults
    mode http
    log global
    option httplog
    timeout connect 5s
    timeout client 60s
    timeout server 60s

frontend docker_api
    bind :2375

    # Read-only calls
    http-request allow if METH_GET { path,url_dec -m reg -i ^/containers/json$ }
    http-request allow if METH_GET { path,url_dec -m reg -i ^/containers/[a-f0-9]{12,64}/(json|logs)$ }
    # Lifecycle calls
    http-request allow if METH_POST { path,url_dec -m reg -i ^/containers/[a-f0-9]{12,64}/(start|stop|restart)$ }

    # Everything else is denied: exec, create, delete, kill, archive, export, images, volumes, ...
    http-request deny

    default_backend docker

backend docker
    server engine /var/run/docker.sock
```

If you only want the read-only features (`/status`, `/logs`, `/checks`) and no
actions, `tecnativa/docker-socket-proxy` is fine with `CONTAINERS=1` and
`POST=0` (everything else left at the default). Be aware that `CONTAINERS=1`
also lets a client read `/containers/{id}/archive` and `/export`, i.e. files of
any container, and that the bot's start/stop/restart will then be refused with
HTTP 403.

### Example `compose.yml`

```yaml
services:
  socket-proxy:
    image: haproxy:3.2-alpine
    restart: unless-stopped
    # haproxy runs as uid 99; give it the group that owns docker.sock
    # (find it with: stat -c %g /var/run/docker.sock)
    user: "99:999"
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    volumes:
      - ./haproxy.cfg:/usr/local/etc/haproxy/haproxy.cfg:ro
      - /var/run/docker.sock:/var/run/docker.sock:ro
    networks: [docker-api]       # internal only; no ports are published

  vps-telegram-bot:
    image: ghcr.io/qtekfun/vps-telegram-bot:0.1.0
    restart: unless-stopped
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    environment:
      DOCKER_HOST: tcp://socket-proxy:2375
      TELEGRAM_TOKEN_FILE: /run/secrets/telegram_token
      ALLOWED_USER_IDS: "123456789"
      ACTION_CONTAINERS: traefik,ntfy,gatus
      GATUS_URL: http://gatus:8080
      STATUS_DISK_PATHS: /data
      MONITOR_STATE_PATH: /state/monitor.json
    secrets: [telegram_token]
    volumes:
      - /srv:/data:ro                       # filesystem reported by STATUS_DISK_PATHS
      - /var/lib/monitor:/state:ro          # optional alert state (directory, read-only)
    networks:
      - docker-api   # reach the socket proxy
      - default      # reach api.telegram.org (and Gatus, if it is on this network)

secrets:
  telegram_token:
    file: /etc/vps-telegram-bot/token       # mode 0400, owned by the container user (65532)

networks:
  docker-api:
    internal: true   # no route to the outside
```

The container user is `nonroot` (UID/GID 65532), so the token file must be
readable by it. The proxy is never published on the host and sits on an
`internal` network that only the bot joins.

## Build and release

```sh
go vet ./... && go test -race ./...
docker build -t vps-telegram-bot .
```

CI (`.github/workflows/ci.yml`) runs vet, tests with `-race`, `golangci-lint`,
`govulncheck` and a multi-arch Docker build on every push and pull request.
Pushing a tag such as `v0.1.0` runs `.github/workflows/release.yml`, which
publishes a `linux/amd64` + `linux/arm64` image to
`ghcr.io/qtekfun/vps-telegram-bot` tagged `0.1.0` and `0.1` (and `1` from v1 on).
Actions are pinned by commit SHA and Dependabot keeps Go modules, Actions and the
Docker base images current.

## Security notes

What the bot can do: list containers and read their state; read the last 200 log
lines of any container (logs can contain sensitive data, which is why only the
allowlisted users can see them); start, stop or restart the containers in
`ACTION_CONTAINERS` after an explicit confirmation; read the host's memory, load,
uptime and the disk usage of the configured paths; read the Gatus status.

What it cannot do: run commands in containers, create or delete containers,
images, volumes or networks, kill processes, or touch containers outside the
allowlist (also enforced by the proxy when you use the configuration above). It
has no inbound port.

If the bot token leaks: anyone with it can impersonate the bot and read the
messages it would receive, but cannot operate your containers, because the bot
only obeys the allowlisted user IDs and the token is not a credential for
Docker. They could, however, send messages as your bot and, if they poll
`getUpdates` themselves, take over its updates. Revoke it right away in
BotFather: `/revoke` (or `/token`, then pick the bot and revoke), put the new
token in the token file and restart the container.

If your Telegram account is compromised, the attacker can use the bot as you.
Enable two-step verification in Telegram, and keep `ACTION_CONTAINERS` to the
containers you really need to operate remotely.

## License

MIT
