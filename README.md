# Book

Self-hosted meeting booking system (Calendly alternative).

## Features

- **Public booking page** — guests choose meeting type, date, time slot, and book
- **Admin panel** — manage meeting types, working hours, bookings, settings (protected by Authentik SSO)
- **Calendar integration** — complete expanded availability reads via [calendar-mcp](https://github.com/Dzarlax-AI/calendar-mcp), with durable booking and cancellation reservations
- **Video calls** — auto-create Google Meet or MS Teams links per meeting type
- **Timezone support** — auto-detected with manual override
- **Per-type availability** — each meeting type can override global working hours with its own schedule
- **Calendar blocking filter** — choose which calendars are checked for busy slots; subscribed (read-only) calendars are labeled
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


## Calendar API contract

Book requires a calendar-mcp version exposing `GET /api/v2/availability/events`. It reads each configured blocking calendar separately with `view=expanded`, follows bounded pagination, and checks page completeness, source status, calendar identity, instance kinds, and event times. Missing or incomplete responses stop availability checks. There is no fallback to `/api/events` or `/api/v2/events`.

Microsoft calendars can participate in availability checks, but Microsoft booking targets currently fail closed before reserving a slot: the legacy create adapter rejects guest attendees without an explicit notification policy. This change does not enable a new invitation policy.

Calendar discovery uses `GET /api/v2/availability/calendars?provider=google|microsoft|apple`. Each response must be complete for that provider and use exact account-scoped IDs. Admin loads the three providers independently, so an unavailable Apple account does not hide available Google calendars. An incomplete admin view is never evidence that a calendar is free. Event creation and deletion retain the existing legacy `POST /api/events` and `DELETE /api/events` contracts; switching availability reads does not change notification or conferencing policies.

Booking targets currently require an exact Google calendar with a positive create capability, notification policy `none`, and conferencing support when video is enabled. Microsoft and Apple can participate in availability checks.

Saved calendar IDs must exactly match the discovered catalog. Book does not guess account mappings or replace old IDs. If an old ID is no longer available, update the meeting type and blocking-calendar settings in the admin panel. For explicit selections, Book checks only providers required by those IDs and the booking target. When the saved selection is empty (All mode), Book requires complete catalogs from all providers before checking every registered calendar; an unavailable provider is not silently omitted. A missing booking target or Calendar API configuration, an unreadable selected calendar, or an availability error returns HTTP 503 instead of offering slots. Admin pages remain available for configuration.

All-day events continue to block days only when their titles match configured keywords. Timed events and existing Book reservations block overlapping slots, including buffers. Conservative availability checks may hide otherwise free slots when the provider cannot prove a complete response.

Admin shows unavailable provider warnings and retains hidden saved selections when other settings are saved. Unavailable selections have a separate explicit removal control: removing one means Book will stop checking that calendar. Select All explicitly or keep at least one selection; an empty checkbox submission does not silently change selected mode to All. An unavailable existing booking target stays selected until explicitly changed.

Settings forms compare both blocking-calendar IDs and all-day keywords atomically. Stale forms return a conflict rather than overwriting newer settings. Booking reservations compare the settings snapshot used for availability under the same lock, so a changed blocking configuration rejects the reservation before any provider write. Target edits also reject a calendar changed since the form was loaded.

## Booking state and recovery

Book checks the chosen slot again on submission, then commits a `pending` reservation before attempting calendar creation once. It records `confirmed` only after receiving a nonempty event ID and successfully updating the reservation. Pending reservations block overlapping bookings across meeting types and count toward daily limits.

A timeout, lost response, empty event ID, or unconfirmed database update requires manual verification. Book retains the reservation and does not automatically resend the calendar operation. The guest page explains the uncertain result and blocks resubmission on that page; the durable reservation protects the slot after a refresh. Do not resolve an uncertain result by submitting a replacement booking.

Cancellation first changes a confirmed reservation to `cancelling`, then attempts one deletion against its recorded calendar and event IDs. It becomes `cancelled` only after deletion and the final database update succeed. An uncertain result requires manual verification and never triggers an automatic repeat deletion. Reservations not confirmed as cancelled continue to block the slot. The admin panel distinguishes these states and does not offer another cancellation for pending or uncertain work.

### Original calendar snapshots

Migration `003_booking_calendar_snapshot.sql` adds `bookings.calendar_id_snapshot`. New reservations record the exact calendar that passed the availability check; a concurrent change to the meeting type's calendar rejects the reservation. Later changes to the meeting type do not redirect creation or cancellation of that booking.

Existing bookings retain a NULL snapshot. The migration does not infer their original calendars from the current meeting type and does not backfill them. Automatic cancellation is unavailable for those bookings: verify the original calendar and event manually before taking action. No automatic recovery or snapshot-repair workflow is provided by this change.

Migrations run before the HTTP server starts. A migration error terminates startup, so a partially upgraded schema is not served as a healthy application.

### Remaining limits

Book serializes its own reservations; it cannot prevent another calendar client from adding a conflicting event after the availability check. Provider operations and the Book database are not one atomic transaction. A crash or uncertain response can therefore leave a pending or cancelling reservation that requires operator review. Calendar-provider notification and video-call behavior still follows the existing write adapters.

## Development

```bash
go mod tidy
go build ./cmd/book
go test -race -count=1 ./...
DATABASE_URL=postgres://... ./book
```

## Deploy

Deploy the required calendar-mcp availability endpoint before switching Book to this client. Older servers cause availability checks to fail closed; Book does not retry another API contract. Back up the Book database before applying migration 003.

Docker image built by GitHub Actions on push to `main`.

Example `docker-compose.yml` with Traefik + Authentik:

```yaml
services:
  book:
    image: ghcr.io/dzarlax/book:latest
    container_name: book
    restart: unless-stopped
    env_file: .env
    networks:
      - traefik
      - infra
    labels:
      - "traefik.enable=true"
      # Public router — booking pages, no auth
      - "traefik.http.routers.book.entrypoints=https"
      - "traefik.http.routers.book.rule=Host(`book.example.com`) && !PathPrefix(`/admin`)"
      - "traefik.http.routers.book.tls=true"
      - "traefik.http.routers.book.tls.certresolver=letsEncrypt"
      - "traefik.http.routers.book.middlewares=book-ratelimit"
      - "traefik.http.routers.book.service=book"
      # Rate limit
      - "traefik.http.middlewares.book-ratelimit.ratelimit.average=20"
      - "traefik.http.middlewares.book-ratelimit.ratelimit.burst=50"
      # Admin router — protected by Authentik ForwardAuth
      - "traefik.http.routers.book-admin.entrypoints=https"
      - "traefik.http.routers.book-admin.rule=Host(`book.example.com`) && PathPrefix(`/admin`)"
      - "traefik.http.routers.book-admin.tls=true"
      - "traefik.http.routers.book-admin.tls.certresolver=letsEncrypt"
      - "traefik.http.routers.book-admin.middlewares=authentik-auth"
      - "traefik.http.routers.book-admin.service=book"
      # Service
      - "traefik.http.services.book.loadbalancer.server.port=8080"

networks:
  traefik:
    external: true
  infra:
    external: true
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
