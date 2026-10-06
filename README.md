# Avari Tabletop (avari-tabletop-backend)

Серверное приложение на Go 1.27.1 для коллекции настольных игр: поиск BoardGameGeek, личные статусы, оценки и заметки, AI-чат и ответы по загруженным правилам.

## Локальный запуск

Нужны Go 1.27.1 и PostgreSQL. Из корня проекта:

```sh
docker compose up -d postgres
go run ./cmd/server
```

Конфигурация по умолчанию подключается к локальному PostgreSQL из `docker-compose.yml`. Сервер доступен на `http://localhost:8080`. Миграции применяются при старте. Зарегистрируйте пользователя через `/register`, затем добавляйте игры через `/games/search`.

Для собственной конфигурации передайте переменные окружения: `DATABASE_URL`, `JWT_SECRET`, `PORT` и `ENV`. Для публичного запуска установите собственный `JWT_SECRET` и `ENV=production`. `go run` сам не загружает `.env`; цели `make run` и `make seed` читают существующий `.env`.

## Коллекция

На `/collection` доступны фильтры Owned/Wishlist/Played, поиск по части названия без учёта регистра и сортировка по времени добавления, названию или личной оценке. Фильтры сохраняются в URL и совместно применяются после редактирования и удаления игры. Игры без оценки стоят в конце сортировки по оценке; пустое поле оценки убирает оценку.

Пример: `/collection?status=owned&q=колонизаторы&sort=rating`. Поиск внутри коллекции использует сохранённые данные и не обращается к BGG. Поиск `/games/search` запрашивает BGG только для выдачи; детали загружаются при открытии или добавлении игры.

## Чат и правила

По умолчанию используются Ollama на `http://localhost:11434`, модель чата `qwen3.5`, embeddings `nomic-embed-text` и локальный векторный индекс `chromem`. Модели должны быть доступны сервису Ollama. Настройки:

- Чат: `LLM_PROVIDER` (`ollama`, `claude`, `openai_compat`), `LLM_MODEL`, `LLM_BASE_URL`, `LLM_API_KEY`. `CLAUDE_API_KEY` поддерживается как совместимая настройка Claude.
- Embeddings: `EMBED_MODEL`, `EMBED_BASE_URL`. В текущей сборке embeddings создаются через Ollama, включая значение `EMBED_PROVIDER=openai_compat`.
- Индекс: `VECTOR_STORE=chromem` (по умолчанию), `RULES_PERSIST_PATH=./data/rules`. Для `VECTOR_STORE=pgvector` нужен PostgreSQL с доступным расширением `vector`; включается соответствующая миграция.

Поиск и управление коллекцией работают без доступного LLM. Для ответов по правилам загрузите текст через кнопку правил в карточке игры. Индексация требует доступного embeddings-сервиса.

## Проверки

Обычные проверки используют локальные HTTP-заглушки и не требуют PostgreSQL, BGG или LLM:

```sh
go test ./... -count=1
go vet ./...
go build ./cmd/server ./cmd/seed
```

Если системный Go-кэш недоступен для записи, задайте `GOCACHE` в доступном временном каталоге.

E2E запускается через `cd tests && npm install && npx playwright install && npx playwright test`. Нужны уже запущенное приложение на порту 8080, отдельная тестовая база, браузеры Playwright, доступ к CDN и, для соответствующих сценариев, BGG/LLM. Тесты создают пользователей; используйте отдельную базу. `make test-rag` дополнительно требует Ollama и модели. `make test-integration` задаёт тег `integration`; отдельного набора тестов с этим тегом сейчас нет.

## Структура

`cmd/server` собирает зависимости; `internal/handler` обрабатывает HTTP/HTMX/SSE; `internal/service` содержит логику; `internal/repository` выполняет SQL. Шаблоны и статика в `web`, миграции в `migrations` встраиваются через `assets.go`.

План реализуемого этапа и результаты проверки: [docs/DEVELOPMENT_PLAN.md](docs/DEVELOPMENT_PLAN.md).


Мобильный auth JSON API `/api/v1` реализован локально: [MOBILE_AUTH.md](docs/MOBILE_AUTH.md). Миграция 004 применяется при старте; web cookies сохраняют прежние routes. Collection/rules/chat JSON пока не реализованы. Для loopback запуска доступны LISTEN_HOST=127.0.0.1 и PORT; dev recovery delivery хранится в приватном DEV_MAILBOX_DIR.

Mobile collection/game JSON API is implemented and verified locally; see [docs/MOBILE_COLLECTION.md](docs/MOBILE_COLLECTION.md). Independent/staging acceptance and real BGG verification remain pending. Flutter collection/editor integration continues as F03.
