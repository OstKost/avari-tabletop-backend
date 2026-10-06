# Mobile collection/game API (B02)

Implemented locally on 2026-10-05; independent review and staging acceptance pending. The mobile source of truth is `../avari-tabletop-mobile/api/openapi.json` and `docs/API_CONTRACT.md`. No user database was modified during verification.

Bearer authentication uses the same live family validation as B01. `/api/v1/meta` enables `collection` when the collection handler is installed; rules/chat remain disabled. JSON errors never redirect or expose provider/SQL errors. All API responses use no-store.

## Operations

- `GET /games/search?q=…`: trimmed 2–120 Unicode characters; one BGG search, at most 100 results, explicit truncated flag, one bounded current-user membership lookup. No per-result `/thing`. Failed membership read gives 503.
- `GET /games/bgg/{bggId}`: shared seven-day catalog cache, nullable own collection entry, `stale:true` for stale-cache fallback. BGG not found gives 404, provider failure 502, timeout 504.
- `GET /collection`: scoped SQL filtering, literal substring query (including `%`/`_`), status and newest/name/rating sort, limit 1–100/default 50. List reads omit descriptions/images. Null ratings sort last; ties use added_at descending then game UUID ascending.
- `POST /collection`: `{bgg_id,status?}`, defaults owned, requires UUID Idempotency-Key. Successful add 201, duplicate 409 with current entry. Durable per-user replay retains original status/entry/request ID for 24 hours. Normalized equivalent bodies replay; mismatches give 409. Replay does not fetch BGG or consume issuance quota.
- `GET /collection/{gameId}`: own entry/ETag. Another user's membership gives 404.
- `PATCH /collection/{gameId}`: requires strong `If-Match: "vN"`. Missing gives 428, stale gives 412 with current entry/ETag. Absent fields stay unchanged, `rating:null` and `notes:""` clear values. Empty patches, null notes/status, invalid ratings and more than 10,000 Unicode note characters are rejected. JSON body limit is 64 KiB so 10,000 UTF-8 characters fit.
- `DELETE /collection/{gameId}`: same precondition semantics, 204 without a body. Rules/chat are not deleted by removing membership.
- `GET /collection/snapshot`: always unfiltered, captures DTOs in one SQL read and stores them privately in PostgreSQL for five minutes. Later pages load the frozen entries/versions; expiry gives 410. Last page alone has complete:true. Maximum ten live snapshots per user, serialized across replicas.

## Migration and concurrency

Paired migration `005_mobile_collection` adds updated_at/version and a shared database sequence/UPDATE trigger. Existing web writes increment versions without handler changes. Re-adds receive new sequence values, preventing reuse of an old ETag. Migration backfills existing records and preserves original collections/games. Down removes only new objects; rollback/reapplication resets version history and requires clients to reload their entries.

`games.name_fold` is written with Go Unicode lowercase by catalog upserts, backfilled with PostgreSQL lowercase plus explicit Cyrillic mapping. SQL uses C collation for stable binary ordering matching web Go string comparisons on tested Cyrillic/Latin fixtures. Notes validation is shared with the web collection service.

HMAC-SHA256 cursors bind route kind, user, normalized filters/sort, page size, offset, expiry and snapshot UUID. Invalid/tampered/cross-user cursors return 400. Live list uses bounded offset pagination (maximum one million); edits between requests can change page membership. Immutable snapshots provide consistency for offline sync. Signing keys derive from JWT_SECRET, so rotation invalidates old cursors.

Conditional mutations lock the owned collection row and compare versions in the transaction. Add locks the user, checks the durable replay, then fetches/upserts the game using the same transaction connection. This avoids nested connection acquisition and works with a one-connection pool. Independent adds for a user serialize; external fetch respects context and the BGG client timeout.

BGG quota is 30 requests/minute/user/process. It is a local limiter, not a shared cluster-wide quota. Snapshot quota and replay state are durable/shared. The cleanup loop deletes expired snapshots/replays each minute; reads enforce TTL even before cleanup. User deletion cascades to both tables.

## Verification

Isolated disposable PostgreSQL 16 database `avari_b01_test` is required through `B01_TEST_DATABASE_URL`; each test uses a separate dropped schema. The name is retained from B01 as a safety guard. uuid-ossp is installed in public so schema cleanup cannot remove it from another fixture.

`go test -race ./... -count=1`, `go vet ./...`, `go build ./cmd/server ./cmd/seed` passed. Eleven B02 database test groups cover lifecycle, replay/concurrency, user scope/search, SQL pagination, immutable snapshots, validation/concurrent patch, migration roundtrip, full-contract response export, one-connection add, quota/cursor binding and provider timeout. The existing web service/handler tests also passed; actual web SQL updates invalidated API ETags.

The mobile full OpenAPI checker validated 14 exported responses covering all eight operations. A separate loopback server launched from cmd/server passed 14 live collection/auth schema checks using a cached synthetic catalog entry. Responses contain no sessions/passwords in exported artifacts. Test tokens remain in memory and are never printed.

Real external BGG access/credentials/availability, staging, browser E2E and production data compatibility are unverified. Existing legacy notes/catalog values need acceptance checks against the mobile field limits before rollout. Flutter collection/editor wiring is F03; native app UI was not changed by B02. Android build remains postponed by the owner. No deployment/commit/push occurred.
