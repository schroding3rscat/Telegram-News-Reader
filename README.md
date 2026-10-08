# Telegram News Reader

Сервис читает Telegram-каналы от имени отдельного пользовательского аккаунта, отбрасывает рекламу,
неинтересные темы и дубликаты, затем публикует сообщения в выбранный канал через бота.

## Возможности

- публичные каналы, username/URL и приватные invite-ссылки через MTProto;
- текст, фото, видео, документы и медиагруппы;
- настоящий forward для доступных боту публичных сообщений;
- copy fallback для приватных источников;
- локальная Qwen3-0.6B через официальный `llama-server`, без облачных API;
- недельная дедупликация текста и медиа;
- обратная связь через inline-кнопки и защищенную админку;
- SQLite, один Go-бинарник с embedded UI;
- Caddy, автоматический HTTPS и systemd.

## One-click установка

Нужна чистая Debian 12/Ubuntu 24.04 VM с x86_64, 2–4 ГБ RAM и минимум 2.5 ГБ свободного места.
До запуска создайте A-запись домена на публичный IPv4 VM и откройте TCP-порты 80 и 443.

```bash
curl -fsSL https://raw.githubusercontent.com/schroding3rscat/Telegram-News-Reader/main/deploy/install.sh | sudo bash
```

Инсталлятор запросит домен, email Let's Encrypt, логин и пароль админки. После проверки DNS он:

1. установит Caddy из официального репозитория;
2. скачает release и проверит SHA-256;
3. скачает Qwen3-0.6B Q4_K_M;
4. создаст изолированного системного пользователя и systemd units;
5. выпустит сертификат и выведет URL мастера настройки.

Неинтерактивный режим:

```bash
curl -fsSL https://raw.githubusercontent.com/schroding3rscat/Telegram-News-Reader/main/deploy/install.sh |
  sudo DOMAIN=news.example.com \
    ACME_EMAIL=admin@example.com \
    ADMIN_USER=admin \
    ADMIN_PASSWORD='change-me' \
    bash -s -- --non-interactive
```

Обновление и удаление:

```bash
curl -fsSL https://raw.githubusercontent.com/schroding3rscat/Telegram-News-Reader/main/deploy/install.sh | sudo bash -s -- --update
curl -fsSL https://raw.githubusercontent.com/schroding3rscat/Telegram-News-Reader/main/deploy/install.sh | sudo bash -s -- --uninstall
```

Удаление сохраняет `/var/lib/telegram-news-reader` и `/etc/telegram-news-reader`. Для удаления данных
добавьте `--purge-data`.

## Первый запуск

Мастер запросит:

- Telegram API ID и API Hash из <https://my.telegram.org/apps>;
- телефон отдельного пользовательского аккаунта и, при наличии, пароль 2FA;
- токен бота от `@BotFather`;
- целевой канал, где бот является администратором;
- Telegram user ID людей, которым разрешены кнопки разметки.

Код авторизации Telegram вводится на странице «Настройки». Пользовательский аккаунт должен соблюдать
лимиты Telegram: массовое автоматическое вступление в каналы может привести к `FLOOD_WAIT` или блокировке.

Для приватного источника Bot API не может сделать настоящий forward, если бот не состоит в канале.
Поэтому сервис скачивает медиа через пользовательскую MTProto-сессию и публикует копию с указанием источника.

## Безопасность

- Fiber слушает только `127.0.0.1`;
- внешний доступ идет через Caddy и HTTPS;
- любой маршрут и asset требует постоянный query token, иначе возвращается `404`;
- после token-проверки требуется Basic Auth;
- POST-запросы защищены SameSite/CSRF token;
- callback-кнопки выполняются только для настроенного allowlist;
- конфиг и MTProto-сессия недоступны другим пользователям ОС.

Query token присутствует в URL. Не отправляйте ссылку админки другим людям и не размещайте внешние
ресурсы на страницах админки.

## Ресурсы

Профиль для VM 2 ГБ использует Qwen3-0.6B Q4_K_M, контекст 2048, один inference slot и один worker.
На VM 4 ГБ модель можно заменить в systemd unit и YAML, не меняя Go-код. При недоступной LLM сообщения
остаются в durable-очереди и повторяются с exponential backoff.

## Разработка

Требуется Go 1.26+:

```bash
cp config.example.yaml config.yaml
go test ./...
go run ./cmd/telegram-news-reader -config ./config.yaml
```

Создание bcrypt-хэша:

```bash
go run ./cmd/telegram-news-reader hash-password 'your-password'
```

Основные каталоги:

- `internal/telegram/user` — MTProto ingestion и загрузка медиа;
- `internal/telegram/bot` — публикация и feedback callbacks;
- `internal/classifier` — OpenAI-compatible клиент `llama-server`;
- `internal/dedup` — fingerprint и недельное окно;
- `internal/pipeline` — durable обработка;
- `internal/web` — Fiber и embedded UI;
- `internal/storage` — SQLite/WAL.

## Резервное копирование

Остановите сервис или используйте SQLite online backup, затем сохраните:

- `/var/lib/telegram-news-reader/reader.db`;
- `/var/lib/telegram-news-reader/telegram.session`;
- `/etc/telegram-news-reader/config.yaml`.

Не публикуйте эти файлы: они содержат доступ к Telegram и админке.
