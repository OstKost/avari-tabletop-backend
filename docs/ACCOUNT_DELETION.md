# Account deletion — B05

Общий service/repository находится в `internal/service/account_deletion_service.go` и `internal/repository/account_deletion_repo.go`, API/web resource — в `internal/handler/account_deletion_*`. Миграция `006_account_deletion` добавляет freeze flag и durable jobs. Worker подключён в `cmd/server/main.go` к cleanup loop. JSON endpoints и web `/account/delete` используют один service.

Полный сценарий, legacy ownerless release blocker, R2 cleanup hook требования и client receipt lifecycle: [mobile account deletion](../../avari-tabletop-mobile/docs/ACCOUNT_DELETION.md).

Для проверок требуется disposable `B01_TEST_DATABASE_URL` с database name `avari_b01_test`. `go test -race ./internal/handler -run AccountDeletion -count=1` проверяет reauthentication, concurrent retry, revoke/freeze, receipt scope, cleanup failure/retry/worker lease, cascade, retention, migration roundtrip и web resource headers. Затем `go test -race ./... -count=1`, `go vet ./...`, `go build ./cmd/server ./cmd/seed`. Live server использует отдельную disposable БД, иначе public application tables загрязняют search_path временных fixtures.

Никакие исторические данные пользователей, ownerless SQL/chromem/pgvector или backups на production не удалялись. Контролируемая историческая очистка и отключение legacy выдачи до релиза требуют отдельного операционного подтверждения.
