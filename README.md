# Book

Self-hosted meeting booking system (Calendly alternative).

## Features

- **Public booking page** — guests choose meeting type, date, time slot, and book
- **Admin panel** — manage meeting types, working hours, bookings, settings (protected by Authentik SSO)
- **Calendar integration** — real-time free/busy via [calendar-mcp](https://github.com/Dzarlax-AI/calendar-mcp) REST API
- **Video calls** — auto-create Google Meet or MS Teams links per meeting type
- **Timezone support** — auto-detected with manual override
- **All-day event filtering** — configurable keywords block entire days (e.g. "public holiday")
- **Rate limiting** — Traefik middleware on public routes

## Stack

- **Backend**: Go + Chi router
- **Frontend**: HTMX + Go `html/template`
- **Database**: PostgreSQL (shared instance, `book` schema)
- **Auth**: Authentik ForwardAuth on `/admin`
- **Calendar**: calendar-mcp internal REST API (port 8081, Docker infra network)
- **Deploy**: Docker, GitHub Actions CI, Traefik reverse proxy

## Configuration

All configuration via environment variables (`.env`):

| Variable | Description |
|---|---|
| `DATABASE_URL` | PostgreSQL connection string |
| `PORT` | HTTP port (default: 8080) |
| `TIMEZONE` | Default timezone (default: Europe/Belgrade) |
| `BASE_URL` | Public URL |
| `CALENDAR_API_URL` | calendar-mcp REST API base URL |
| `CALENDAR_API_KEY` | calendar-mcp API key |

See `.env.example` for reference.

## Development

```bash
go mod tidy
go build ./cmd/book
DATABASE_URL=postgres://... ./book
```

## Deploy

Docker image built by GitHub Actions on push to `main`:

```bash
docker compose pull && docker compose up -d
```

## Project Structure

```
cmd/book/main.go        — entry point, routing
internal/
  config/               — env-based configuration
  handler/              — HTTP handlers (public + admin)
  model/                — data types
  storage/              — PostgreSQL queries
  calendarapi/          — calendar-mcp REST client
  ui/static/            — CSS
  ui/templates/         — Go HTML templates + HTMX
migrations/             — SQL schema
```
