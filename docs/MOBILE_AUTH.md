# B01: мобильный JSON API

Реализован локально 2026-10-05. Независимая приёмка и staging запуск ещё не выполнены. Мобильный контракт и fixtures находятся в соседнем `../avari-tabletop-mobile/api/`.

## Маршруты

Префикс `/api/v1`: GET `/meta`, POST `/auth/register`, `/auth/login`, `/auth/refresh`, `/auth/logout`, `/auth/password-reset/request`, `/auth/password-reset/confirm`, GET `/me`. Collection/rules/chat JSON routes ещё отсутствуют; `/meta.capabilities` для них возвращает false.

Request/response: snake_case JSON, `Cache-Control: no-store`, request UUID, безопасные errors. Неподдерживаемые API routes/methods тоже возвращают JSON. UserDTO содержит только id/email/username/created_at. Cookie credentials не авторизуют mobile API; mobile access не принимается вебом.

Access JWT действует 15 минут, family — 30 дней с момента создания. Проверяются HS256, expiry, issuer/audience, token_use, sid, наличие пользователя, текущая auth_version и состояние family в БД. Refresh/reset tokens — 256-bit randomness; БД хранит SHA-256 hashes. Refresh ротация под блокировками PostgreSQL сериализует конкурентные запросы. Повтор consumed refresh с тем же attempt UUID в течение 60 секунд возвращает тот же ответ; другой attempt или выход за окно отзывает family. Replay response шифруется AES-GCM ключом, производным от JWT_SECRET с отдельным префиксом, и привязан к family через AAD. Ключ должен сохраняться между перезапусками/instances.

Миграция `004_mobile_auth` добавляет auth_version, mobile_sessions, mobile_refresh_tokens и password_reset_tokens. Down сохраняет пользователей и остальные продуктовые данные; отзывает возможность использования новой auth схемы. Применяется существующим migration runner при старте. PostgreSQL cleanup раз в минуту удаляет ciphertext за пределами replay window и истёкшие sessions/reset tokens. Consumed refresh hashes сохраняются до истечения family для выявления reuse.

Веб по прежнему отдаёт страницы/HTMX и cookie redirects. Новые web JWT содержат auth_version; прежние JWT без этой версии принимаются только пока user.auth_version=0. Password reset увеличивает версию и атомарно отзывает все mobile families, поэтому прежние web/mobile JWT не проходят. Удалённый user также не проходит. Веб middleware теперь проверяет пользователя в БД: недоступность БД даёт 503 без удаления cookie.

## Почта и лимиты

Только ENV=development включает fake mailer. DEV_MAILBOX_DIR по умолчанию `.cache/dev-mailbox`, directory 0700 / delivery files 0600; сырые reset tokens и email не логируются и не доступны через HTTP. JSON delivery содержит email/reset_token/expires_at; окно 15 минут, старые delivery files удаляются при следующей отправке, mailbox ограничен 1000 файлами. Каталог исключён из Git. Для проверки формы использовать внутренний маршрут клиента `/reset-password?token=...`; внешний app/web link домен ещё не настроен.

Known/unknown email дают одинаковый 202 accepted. Ошибка доставки не раскрывает существование аккаунта. Для production/staging mailer отключён: recovery request равномерно возвращает 503, пока не реализована настоящая доставка. Это остаётся условием внешнего выпуска.

Лимиты в памяти процесса, максимум 10000 buckets. Public auth — 10/min по RemoteAddr; reset дополнительно 3/hour по хешу email; refresh lookup — 120/min по RemoteAddr, issuance — 30/min на пользователя. Replay bypasses issuance limit. X-Forwarded-For не принимается как доверенный источник IP. Распределённый limiter/proxy trust для публичного staging пока не настроены.

## Проверки

Обычные `go test ./...`, `go vet ./...`, `go build ./cmd/server ./cmd/seed` — PASS. PostgreSQL checks используют только database `avari_b01_test`, каждому тесту создают/удаляют отдельную schema. Без B01_TEST_DATABASE_URL явно skip.

```sh
B01_TEST_DATABASE_URL=<disposable-avari_b01_test-connection> go test -race ./... -count=1
```

8 DB test groups: lifecycle, concurrent replay/reuse across instances, password reset/web+mobile revocation, expiry/deletion/cleanup, JSON/rate limits/mail failure, down/up migration, reset-vs-refresh race, UTF-8/reset expiry. File mailer permissions/retention проверены отдельным unit test.

Для сквозной проверки поднять этот сервер на **отдельной тестовой базе** и loopback port; LISTEN_HOST=127.0.0.1 ограничивает bind, PORT выбирается отдельно. Общие scripts и native Flutter integration:

```sh
# Из ../avari-tabletop-mobile; только disposable API с dev mailbox:
.venv/bin/python tools/contract/check_live_auth.py --base-url http://127.0.0.1:<port>/api/v1 --mailbox <DEV_MAILBOX_DIR>
python3 tools/ios_flutter.py test integration_test/auth_api_test.dart -d <simulator-id> --dart-define=B01_API_URL=http://127.0.0.1:<port>/api/v1
```

Проверены 15 live JSON responses по полным OpenAPI schemas и native iOS registration → Keychain → refresh restore → me → logout → revoked 401. Только synthetic accounts на disposable database. Integration меняет local installation marker тестового приложения; после тестов обычный app собран/возвращён в Simulator. Физические устройства, реальные external recovery links, удалённый CI/staging и настоящая почта остаются unverified.

Go CI добавлен в `.github/workflows/go.yml` с отдельным PostgreSQL service, race tests, vet и build; удалённый запуск не выполнялся.
