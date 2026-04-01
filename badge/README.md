# Livedocs Badge Endpoint

Cloudflare Worker that serves SVG badges showing documentation drift status.

## Setup

```bash
cd badge
npm install

# Create KV namespace
wrangler kv namespace create BADGE_KV
# Copy the returned ID into wrangler.toml

# Set the API token (used by GitHub Action to POST results)
wrangler secret put BADGE_API_TOKEN

# Deploy
npm run deploy
```

## Endpoints

### GET /badge/:owner/:repo.svg

Returns an SVG badge. Colors:

- **Green** — 0 stale references ("Fresh")
- **Yellow** — 1-3 stale references
- **Red** — 4+ stale references
- **Grey** — no data ("unknown")

### POST /badge/:owner/:repo

Updates cached drift results. Requires `Authorization: Bearer <token>` header.

Body:

```json
{
  "total_stale": 2,
  "drift_score": 3,
  "timestamp": "2026-04-01T12:00:00Z"
}
```

## Usage in README

```markdown
![AI Context](https://livedocs-badge.your-account.workers.dev/badge/owner/repo.svg)
```

## GitHub Action Integration

Add to your workflow:

```yaml
- uses: live-docs/live_docs@v1
  with:
    badge-api-url: https://livedocs-badge.your-account.workers.dev
    badge-api-token: ${{ secrets.LIVEDOCS_BADGE_TOKEN }}
```
