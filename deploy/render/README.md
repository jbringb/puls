# Deploy puls on Render

Puls uses its Postgres backend on Render so the database survives redeploys and
supports multiple instances. The Render managed Postgres is wired automatically
via the `DATABASE_URL` env var defined in `render.yaml`.

## One-time setup

### 1. Create the Blueprint

1. Go to **dashboard.render.com → Blueprints → New Blueprint Instance**.
2. Connect your fork of this repo. Render reads `render.yaml` from the root.
3. Render creates two resources: a **Web Service** (`puls`) and a **PostgreSQL**
   database (`puls-db`).

### 2. Set the secrets

In the **puls** Web Service → Environment, set the three secrets that are marked
`sync: false` in `render.yaml`:

| Key | Value |
|-----|-------|
| `PULS_JWT_SECRET` | A random string, at least 32 characters |
| `PULS_ADMIN_SECRET` | A separate random string, at least 16 characters |
| `PULS_ALLOWED_ORIGINS` | Your service URL, e.g. `https://puls.onrender.com` |

Generate secrets with:
```bash
openssl rand -base64 32   # JWT secret
openssl rand -base64 24   # Admin secret
```

### 3. Wire up GitHub Actions (optional — enables wait-for-live polling)

In the Render **puls** service → Settings → Deploy Hook, copy the URL.

In your GitHub repo → Settings → Environments, create an environment named
`render` and add these secrets:

| Secret | Required | Where to find it |
|--------|----------|------------------|
| `RENDER_DEPLOY_HOOK_URL` | Yes | Render → Service → Settings → Deploy Hook |
| `RENDER_API_KEY` | No (enables polling) | Render → Account Settings → API Keys |
| `RENDER_SERVICE_ID` | No (enables polling) | Render → Service URL (e.g. `srv-abc123`) |

With only the hook URL, the workflow triggers a deploy and exits immediately.
With all three, it polls until the deploy goes live (up to 15 min).

## Manual deploys

**Actions → Deploy · Render → Run workflow**

`autoDeploy: false` in `render.yaml` means pushes to `main` do not deploy
automatically. All production deploys are intentional, triggered from GitHub.

## Checking health

```bash
# Liveness — is the process up?
curl https://puls.onrender.com/healthz

# Readiness — is the database reachable?
curl https://puls.onrender.com/readyz
```

## Local run with Docker Compose

Requires `PULS_JWT_SECRET` and `PULS_ADMIN_SECRET` in a `.env` file (or the
environment). Compose starts Postgres, waits for it to be healthy, then starts puls.

```bash
cat > .env <<EOF
PULS_JWT_SECRET=$(openssl rand -base64 32)
PULS_ADMIN_SECRET=$(openssl rand -base64 24)
EOF

docker compose up --build
```

The server is available at `http://localhost:8080`.

## Notes

- The free-tier Render Postgres instance is limited to 256 MB RAM / 1 GB storage
  and is suspended after 90 days of inactivity. Upgrade the `plan` in `render.yaml`
  to `starter` or higher for production use.
- The free-tier Web Service spins down after 15 minutes of inactivity. The first
  request after sleep takes a few seconds. Upgrade to keep it always-on.
