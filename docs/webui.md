# Client and admin web UIs

The Bridge client and Network Host admin are embedded in their Go binaries.
No frontend build, CDN, or separate web server is needed at runtime.

## Start both

From the repository checkout:

```sh
docker compose up -d --build
```

| Service | Default URL | Purpose |
|---|---|---|
| Bridge client | http://localhost:8080 | Connect RomM, browse the local library, import, view activity, join a Swarm |
| Host admin | http://localhost:8082 | Create Swarms, issue invitations, upload reference catalogues, manage Bridges |
| Host API | http://localhost:8081 | Enrollment and inventory API used by Bridges |

For a remote Docker host, replace localhost with that host's address in your
browser. If these ports are already occupied, change the left-hand side of the
Compose port mappings.

1. Open the Host admin and create its owner account.
2. Create a Swarm. Upload an appropriate Logiqx DAT under Reference catalogues
   for each platform you want Bridges to verify.
3. Issue an invitation, choosing its maximum uses and expiry. Copy the code
   before leaving the page; it is shown once.
4. Open the Bridge client and complete setup with your RomM URL, Client API
   Token, and a local admin password. The RomM URL must be reachable from the
   Bridge container.
5. Open Swarm in the client. Use the **Host API URL**, not the admin UI URL.
   For the included Compose network, this is `http://host:8081`.
   For separate machines, use the Host's reachable API address.
6. Redeem the invitation and follow inventory publishing on the Swarm page.
   The client fetches reference catalogues from the Host before publishing.

The Compose volumes preserve the Bridge configuration, journal, and Host
database across rebuilds. A normal update is another
`docker compose up -d --build`.

## Available workflows

### Bridge client

- Dashboard: RomM connection, Swarm membership, active and review counts from
  the 50 most recent journal entries, recent activity, and links to the next step.
- My library: case-insensitive title and filename search, platform filtering,
  title/platform/size sorting, 100-item pages, and explicit Load more / Retry.
  Sorting works on a copy of the cached inventory. A cold listing can still
  take time because the Bridge reads RomM's inventory; the page shell appears
  immediately and reports loading/error states.
- Downloads: stream files from the operator's own RomM server.
- Inbox: upload progress, inline validation errors, and a link to the accepted
  transfer. Browser uploads are limited to 1 GiB and temporary multipart files
  are cleaned up. Mounted inbox originals are retained.
- Activity: live updates without page reloads, status/search filters, and
  per-transfer history. Polling pauses in hidden tabs; a completed detail view
  stops polling.
- Swarm: enrollment, connection testing, publish progress, and errors. Publishing
  controls stay disabled while a publish is running.
- Settings: connection testing, saved credentials, display name, and password.
  This daemon's imports use the RomM API. The former filesystem selection was
  removed from this UI because the daemon did not honor it.

### Network Host admin

- Swarm cards with enrolled Bridge, published distinct-file, and reference
  catalogue counts.
- Invitations with 1-100 uses, 1-30 day expiry, and accurate active, exhausted,
  expired, or revoked labels. Empty API form fields retain the previous
  single-use / seven-day defaults.
- Copyable one-time codes, including an HTTP clipboard fallback.
- Search/filter Bridge roster, rename, revoke, re-enroll, and remove controls.
- Reference catalogue upload/removal and Swarm deletion, including deletion
  when a reference catalogue exists.
- Responsive navigation and horizontally scrollable tables.

Both interfaces use the existing session authentication. Dynamic content is
escaped by Go templates or inserted with DOM text APIs. HTML, API responses,
and one-time codes are served with Cache-Control: no-store.

## Current boundaries

The client is the local Bridge operator's interface, not a separate multi-user
member portal. This change does not implement cross-Bridge downloads, transfer
grants, a federation request queue, or a searchable central game catalogue.

Published holdings are snapshots. An active credential is not evidence that a
Bridge is online, and duplicate holdings do not establish independent replicas
or preservation health. The admin labels these facts separately.

The verifier currently supports the initial cartridge platforms (gb, gbc, gba,
nds, genesis). RomM can list additional platforms that this verifier cannot
import. RomM's real upload/indexing behavior still requires testing against the
operator's own instance; browser fixtures do not prove that integration.

## Validation

```sh
go test ./...
docker compose run --rm test
```

CI runs gofmt, vet, Go tests, the real PostgreSQL suite, Docker smoke tests, and
Chromium workflows in `tests/browser/webui.mjs`.
The browser job uses a test-only Bridge backend with synthetic records and
scripted imports, plus the real Host daemon and PostgreSQL. It exercises
login, pagination, search, failed-request retry, import feedback, live activity,
publish feedback, mobile overflow, invitation redemption, roster management,
catalogue upload, Swarm deletion, and logout.

Browser screenshots are attached to the workflow as
`webui-browser-screenshots`. They contain fixture data and exclude the
one-time secret page. Browser test code is not compiled into production images.

The Postgres readiness checks use TCP explicitly. This avoids accepting the
temporary socket-only server used during first-time database initialization.
