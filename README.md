# Commons

A self-hosted operations platform for organizations. Commons ties together separate third party software applications used for meetings, member communication, and calendar management into simple user interfaces to manage and trigger customized workflows in one location. Separate integrations of these applications allow for custom implementations for what's needed, all compiled into a single Go binary with a Postgres database.

Commons provides a web-based member portal, an admin UI, a REST API, and a plugin system for adding integrations without touching the core codebase.

Member-facing interaction currently runs through Slack (the production-tested path) or the web portal. Discord and Matrix plugins are implemented but untested — see the individual integration READMEs for status.

---

## Features

- **Calendar and meetings** — manage calendars with recurring events, import from external sources, schedule Zoom meetings from the admin UI or member portal.
- **Webhook automation** — a visual pipeline editor for processing incoming webhooks and internal events. Chain together filters, actions, and conditional logic with `{{key}}` template variables.
- **Role-based access control** — fine-grained permissions, roles, channel approvers, and member promotion workflows.
- **Plugin architecture** — every integration (Slack, Zoom, Discord, YouTube, Google Drive, S3, Nextcloud, Matrix, Vimeo) is a self-registering plugin. Add or remove integrations by importing packages in `main.go`.

---

## Quickstart

### Prerequisites

- [Docker](https://docs.docker.com/get-docker/) and [Docker Compose](https://docs.docker.com/compose/install/)
- A server with a public IP and a domain name — HTTPS is needed if you use integrations that receive webhooks or OAuth callbacks (most do)

### Steps

1. **Clone the repository**

   ```bash
   git clone <repo-url> commons
   cd commons
   ```

2. **Create your environment file**

   ```bash
   cp .env.example .env
   ```

   Edit `.env` and set a strong Postgres password:

   ```bash
   POSTGRES_PASSWORD=$(openssl rand -hex 16)
   ```

   Write it into the `.env` file — Docker Compose reads from there.

3. **Start the stack**

   ```bash
   docker compose up -d
   ```

   This starts Postgres 16 and the Commons app. The `entrypoint.sh` script automatically generates `SESSION_SECRET` and `ENCRYPTION_KEY` on first run and persists them to `/data/secrets.env` so they survive restarts.

4. **Complete the setup wizard**

   The app listens on `http://127.0.0.1:8080` (inside the container on `:8080`). With a reverse proxy in front (see below), visit your domain and you'll be redirected to `/install` to create the first admin account and set your organization name.

---

## Self-hosted deployment

### Reverse proxy (HTTPS)

The app binds to `127.0.0.1:8080` by default. You need a reverse proxy in front to terminate TLS. Most integrations receive webhooks or OAuth callbacks over HTTPS, so this is effectively required for any real deployment — though the app itself will run over plain HTTP for local development (set `SECURE_COOKIES=false`).

#### NGINX Proxy Manager (recommended — web UI with automatic HTTPS)

[NGINX Proxy Manager](https://nginxproxymanager.com/) (NPM) is a Docker-based reverse proxy with a web UI for managing proxy hosts and Let's Encrypt certificates. It's the simplest option if you're already running Commons in Docker — no config files to write.

Point your domain's A/AAAA record at the server first — NPM needs DNS to resolve before it can request a certificate.

See the [Docker Compose with NPM](#docker-compose-with-a-reverse-proxy) example below to run NPM alongside Commons. Once the stack is up:

1. Open the NPM admin UI at `http://your-server-ip:81`
2. Log in with the default credentials (`admin@example.com` / `changeme`) and set a new password
3. Go to **Hosts → Proxy Hosts → Add Proxy Host**
4. Set **Domain Names** to your domain (e.g. `yourdomain.org`)
5. Set **Forward Hostname** to `app` and **Forward Port** to `8080`
6. Under **SSL**, select **Request a new SSL Certificate**, enable **Force SSL**, agree to the Terms, and save

NPM provisions and renews the certificate automatically. Commons is now reachable over HTTPS.

#### Caddy (alternative — automatic HTTPS)

Caddy provisions and renews Let's Encrypt certificates automatically with a minimal config file.

Create a `Caddyfile`:

```
yourdomain.org {
    reverse_proxy localhost:8080
}
```

Install Caddy and start it:

```bash
# macOS
brew install caddy
caddy run --config Caddyfile

# Debian/Ubuntu
sudo apt install caddy
sudo systemctl enable --now caddy
# Place the Caddyfile at /etc/caddy/Caddyfile
```

#### Nginx (alternative — manual config)

If you already run Nginx, use a config like:

```nginx
server {
    listen 443 ssl http2;
    server_name yourdomain.org;

    ssl_certificate     /etc/letsencrypt/live/yourdomain.org/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/yourdomain.org/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Use [certbot](https://certbot.eff.org/) to obtain certificates.

### Docker Compose with a reverse proxy

If you run the proxy in Docker alongside Commons, put all services in the same Compose file so they share a network. A minimal example with NGINX Proxy Manager:

```yaml
services:
  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: commons
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      timeout: 5s
      retries: 5

  app:
    build: .
    depends_on:
      db:
        condition: service_healthy
    environment:
      DATABASE_URL: postgres://postgres:${POSTGRES_PASSWORD}@db:5432/commons?sslmode=disable
      PORT: "8080"
      SESSION_SECRET: ${SESSION_SECRET:-}
      ENCRYPTION_KEY: ${ENCRYPTION_KEY:-}
    volumes:
      - appdata:/data
    expose:
      - "8080"
    restart: unless-stopped

  proxy:
    image: jc21/nginx-proxy-manager:latest
    ports:
      - "80:80"
      - "81:81"
      - "443:443"
    volumes:
      - npm_data:/data
      - npm_letsencrypt:/etc/letsencrypt
    depends_on:
      - app
    restart: unless-stopped

volumes:
  pgdata:
  appdata:
  npm_data:
  npm_letsencrypt:
```

The app is not exposed on the host — NPM reaches it over the shared Docker network as `app:8080`. Configure the proxy host in the NPM admin UI as described above.

### Binding to a different port

To run Commons on a different host port (e.g. if 8080 is taken):

```bash
HOST_PORT=8081 docker compose up -d
```

### Data persistence

Two Docker volumes are used:

| Volume    | Mount point                | Contents                                                             |
| --------- | -------------------------- | -------------------------------------------------------------------- |
| `pgdata`  | `/var/lib/postgresql/data` | Postgres data directory                                              |
| `appdata` | `/data`                    | `secrets.env` (auto-generated `SESSION_SECRET` and `ENCRYPTION_KEY`) |

**Back up both volumes.** If you lose `appdata`, you lose the encryption key and cannot decrypt stored credentials. If you lose `pgdata`, you lose all data.

### Upgrading

```bash
git pull
docker compose build
docker compose up -d
```

Migrations run automatically on startup. Core migrations run first, then plugin migrations. Both are tracked in the `schema_migrations` table and are idempotent.

---

## Environment variables

### Required (auto-generated by `entrypoint.sh` in Docker)

These are generated on first run and persisted in `/data/secrets.env`. You only need to set them manually if running outside Docker.

| Variable         | Description                                                                                                                                                          |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `DATABASE_URL`   | Postgres connection string, e.g. `postgres://postgres:pass@host:5432/commons?sslmode=disable`                                                                        |
| `SESSION_SECRET` | Secret used to sign session cookies. Auto-generated as 32 random bytes (hex).                                                                                        |
| `ENCRYPTION_KEY` | 32-byte AES-256 key, hex-encoded (64 chars). Used to encrypt sensitive config values in the database. Auto-generated. Generate manually with `openssl rand -hex 32`. |

### Docker Compose

| Variable            | Default | Description                                              |
| ------------------- | ------- | -------------------------------------------------------- |
| `POSTGRES_PASSWORD` | —       | Password for the Postgres `postgres` user. **Set this.** |
| `HOST_PORT`         | `8080`  | Host port to bind the app to.                            |
| `DB_PORT`           | `5432`  | Host port to bind Postgres to.                           |

### Optional

| Variable         | Default | Description                                                                        |
| ---------------- | ------- | ---------------------------------------------------------------------------------- |
| `PORT`           | `8080`  | Port the app listens on inside the container.                                      |
| `INSTALL_MODE`   | `false` | Set to `true` to force the setup wizard at `/install` even when admin users exist. |
| `SECURE_COOKIES` | `true`  | Set to `false` to disable the `Secure` flag on cookies for local HTTP development. |

---

## Configuration

Commons uses a two-tier configuration model:

1. **Bootstrap config** — environment variables loaded at startup (see above). These are needed to start the server and connect to the database.

2. **Dynamic config** — service credentials (API tokens, OAuth secrets, storage keys, etc.) stored encrypted in the `config_store` database table. Managed through the admin UI at **Integrations**. Sensitive values are encrypted with `ENCRYPTION_KEY` and stored with an `enc:v1:` prefix.

Integration credentials are never put in `.env` or environment variables — they live in the database and can be updated at runtime without a restart.

---

## Project structure

| Directory        | Purpose                                                                   | Docs                                     |
| ---------------- | ------------------------------------------------------------------------- | ---------------------------------------- |
| `api/`           | REST API handlers under `/api/` (session-authenticated)                   |                                          |
| `config/`        | Bootstrap env-var config loading                                          |                                          |
| `db/`            | Connection pool, core + plugin migration runner                           | [db/README.md](db/README.md)             |
| `db/migrations/` | Core SQL migration files (`NNN_name.sql`)                                 | [db/README.md](db/README.md)             |
| `events/`        | Event pipeline dispatch for internal triggers                             | [webhooks/README.md](webhooks/README.md) |
| `install/`       | Setup wizard served at `/install` when no admin exists                    |                                          |
| `integrations/`  | One subdirectory per integration (Slack, Zoom, Discord, etc.)             | See below                                |
| `internal/`      | Shared helpers (HTTP testing, pipeline utilities, test DB setup)          |                                          |
| `jobs/`          | Scheduled job logic (meeting reminders, meeting/membership sync, etc)     |                                          |
| `legislation/`   | Legislative bill tracking and sync                                        |                                          |
| `permissions/`   | Permission model                                                          |                                          |
| `platform/`      | Interfaces shared across integrations (Notifier, RecordingStreamer, etc.) | [plugin/README.md](plugin/README.md)     |
| `plugin/`        | Plugin registry, PluginContext, InitAll, scheduled job framework          | [plugin/README.md](plugin/README.md)     |
| `store/`         | Database access layer — all SQL queries live here                         |                                          |
| `util/`          | Miscellaneous utilities                                                   |                                          |
| `web/`           | HTTP middleware, sessions, auth, HTMX/templ assets                        |                                          |
| `web/adminui/`   | Admin UI page handlers                                                    |                                          |
| `webhooks/`      | Generic webhook processing and trigger pipelines                          | [webhooks/README.md](webhooks/README.md) |

### Integrations

Each integration has its own README with setup instructions, config keys, and troubleshooting:

| Integration                                              | Purpose                                                                                |
| -------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| [Slack](integrations/slack/README.md)                    | Member portal and notification hub (production-tested)                                 |
| [Zoom](integrations/zoom/README.md)                      | Meeting recording webhooks and meeting management (production-tested)                  |
| [Discord](integrations/discord/README.md)                | Alternative member portal and notifications (scaffold/WIP — untested)                  |
| [Matrix](integrations/matrix/README.md)                  | Alternative member portal and notifications (scaffold/WIP — untested)                  |
| [YouTube](integrations/youtube/README.md)                | Recording upload to YouTube (production-tested)                                        |
| [Vimeo](integrations/vimeo/README.md)                    | Alternative recording upload host (scaffold/WIP — untested)                            |
| [Google Drive](integrations/gdrive/README.md)            | Recording file backup                                                                  |
| [S3 Storage](integrations/s3storage/README.md)           | Recording backup to S3 / R2 / B2 / MinIO (scaffold/WIP — untested)                     |
| [Nextcloud](integrations/nextcloud/README.md)            | Recording backup to Nextcloud (scaffold/WIP — untested)                                |
| [Google](integrations/google/README.md)                  | Shared OAuth wrapper for YouTube + Drive                                               |
| [LibraryThing](integrations/librarything/README.md)      | ISBN metadata for the library feature (scaffold/WIP — untested)                        |
| [Solidarity Tech](integrations/solidaritytech/README.md) | Solidarity Tech CRM profile lookup + custom property actions (scaffold/WIP — untested) |

---

## Development

### Prerequisites

- Go 1.25+
- Postgres 16 (running locally or via Docker)
- [templ](https://github.com/a-h/templ) CLI v0.3.1001: `go install github.com/a-h/templ/cmd/templ@v0.3.1001`
- [air](https://github.com/air-verse/air) for live reload (optional): `go install github.com/air-verse/air@latest`

### Local setup

1. Start Postgres:

   ```bash
   docker compose up -d db
   ```

2. Copy `.env.example` to `.env` and set `POSTGRES_PASSWORD`. For local dev you also need to set `SESSION_SECRET` and `ENCRYPTION_KEY` (the entrypoint only runs inside Docker):

   ```bash
   POSTGRES_PASSWORD=changeme
   SESSION_SECRET=$(openssl rand -hex 32)
   ENCRYPTION_KEY=$(openssl rand -hex 32)
   DATABASE_URL=postgres://postgres:changeme@localhost:5432/commons?sslmode=disable
   SECURE_COOKIES=false
   ```

3. Run the server:

   ```bash
   go run .
   ```

   Or with live reload (uses `.air.toml`, which is gitignored — create your own):

   ```bash
   air
   ```

### Templ templates

UI templates use [a-h/templ](https://github.com/a-h/templ) v0.3.1001. Source files are `.templ` in `web/templ/` and `web/htmx/`. Generated `_templ.go` files are committed to the repo.

If you edit a `.templ` file, regenerate before building:

```bash
templ generate
```

The Dockerfile runs `templ generate` automatically during the build.

### Running tests

```bash
go test ./...
```

Most tests need a local Postgres. They create throwaway databases via `testhelpers.SetupTestDB(t)`, which connects to `TEST_DATABASE_URL` (or a local default), creates a uniquely-named DB, runs all migrations, and tears it down after.

```bash
TEST_DATABASE_URL="postgres://postgres:pass@localhost:5432/postgres?sslmode=disable" go test ./...
```

Run a single package's tests:

```bash
go test ./store/... -run TestFoo
```

### Building

```bash
go build -o ./tmp/main .
```
