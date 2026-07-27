# Per-user выдача подписок через Authentik

Как каждому сотруднику выдать **личную** ссылку-подписку vlr, привязанную к его
учётке Authentik, без ручной возни.

## Модель

```
ВЫДАЧА (on-demand):
  сотрудник → https://sub.genomed-security.ru → forward_auth (Authentik)
            → vlr /me читает X-Authentik-Email → находит/СОЗДАЁТ юзера
            → персональная страница: QR + link.infrashark.tech/base64/<token>

ОТЗЫВ (event-driven webhook):
  Authentik: юзер деактивирован/удалён → Notification Rule → Webhook
           → POST /v1/authentik/event {email, is_active} → vlr удаляет юзера
```

Выдача и отзыв автоматические и **событийные**: первый вход создаёт доступ,
деактивация в Authentik мгновенно шлёт вебхук и забирает его. Никакого поллинга —
Authentik сам пушит событие. Руками — ничего.

> Опросного reconcile по умолчанию **нет** (он гонял бы весь список юзеров каждые
> N секунд впустую). Есть опциональный backstop-поллинг (по умолчанию выключен) на
> случай пропущенного вебхука — см. ниже.

Три факта, на которых всё держится:

1. **Ключ связки — email.** Логин в Authentik = рабочая почта, и она же кладётся
   в `email` пользователя vlr. Один человек в Authentik ↔ один пользователь vlr.
2. **Токен генерит vlr, не Authentik.** У каждого юзера свой opaque `sub_token`
   (128 бит). В URL он не подставляется из Authentik — `meta_launch_url` умеет
   только top-level поля (`%(email)s`), но не вложенные атрибуты. Поэтому токен
   отдаётся **страницей за forward_auth**, а не через плитку-диплинк.
3. **Плитка у всех одинаковая** (`https://sub.genomed-security.ru`). Персональность
   даёт аутентифицированная сессия, а не URL. Выключил юзера в Authentik →
   forward_auth перестаёт пускать → доступа к странице нет.

## Что настроить

### 1. Authentik

**Provider** (Applications → Providers → Create → **Proxy Provider**):

| Поле | Значение |
|---|---|
| Name | `Provider for vlr-sub` |
| Authorization flow | ваш `default-provider-authorization-implicit-consent` |
| Type | **Forward auth (single application)** |
| External host | `https://sub.genomed-security.ru` |

**Application** (Applications → Applications → Create):

| Поле | Значение |
|---|---|
| Name | `Личный VPN` |
| Slug | `vlr-sub` |
| Provider | `Provider for vlr-sub` |
| Launch URL | `https://sub.genomed-security.ru` |

**Outpost** (Applications → Outposts → **authentik Embedded Outpost** → Edit):
добавьте `Provider for vlr-sub` в список Applications. Без этого шага outpost не
знает про домен и forward_auth вернёт 403 на всё.

Доступ ограничивается как обычно — политикой/группой на Application (например,
группа `vpn-users`). Нет политики → пускает всех аутентифицированных.

### 2. Caddy (на EDGE)

Импортируйте `deploy/caddy/sub.genomed-security.ru.Caddyfile`, замените
`VLR_NODE_ADDR` на приватный адрес узла vlr (NetBird/WG/VLAN — **не** публичный),
`caddy reload`. Добавьте DNS `sub.genomed-security.ru → 185.25.9.41`.

`copy_headers` перезаписывает `X-Authentik-Email` значением от outpost — браузер
подделать его не может. Узел vlr должен слушать `:9777` только в приватной сети.

### 3. Узел vlr

```bash
vlr init --sub-base-url https://link.infrashark.tech \
         --authentik-webhook-secret "$(openssl rand -hex 24)"
```

Или в `config.json`:
```json
"sub_base_url": "https://link.infrashark.tech",
"portal_header": "X-Authentik-Email",
"authentik": {
  "enabled": true,
  "webhook_secret": "<длинный-случайный-секрет>"
}
```

Портал `GET /me` включается, когда задан `sub_base_url`. Эндпоинт отзыва
`POST /v1/authentik/event` включается, когда `authentik.enabled` и задан
`webhook_secret`. Запишите секрет — он же пойдёт в Header-mapping Authentik.

### 4. Webhook в Authentik (авто-отзыв)

Три объекта: Body-mapping (что слать), Header-mapping (секрет), Transport (куда),
Rule + Policy (когда).

**Notification Webhook Mapping — Body** (Customization → Property Mappings →
Create → *Notification Webhook Mapping*, name `vlr-revoke-body`):

```python
event = request.context.get("event")
model = event.context.get("model", {})
from authentik.core.models import User
user = User.objects.filter(pk=model.get("pk")).first()
return {
    "action": event.action,                                  # model_updated | model_deleted
    "email": user.email if user else model.get("name"),      # на delete юзера уже нет → name (у нас = email)
    "is_active": bool(user.is_active) if user else False,
}
```

**Notification Webhook Mapping — Header** (`vlr-revoke-header`):

```python
return {"Authorization": "Bearer <тот-же-webhook_secret>"}
```

**Transport** (Events → Notification Transports → Create → *Webhook (generic)*,
name `vlr-webhook`):

| Поле | Значение |
|---|---|
| Webhook URL | `http://VLR_NODE_ADDR:9777/v1/authentik/event` (приватная сеть) |
| Webhook Mapping (Body) | `vlr-revoke-body` |
| Webhook Mapping (Header) | `vlr-revoke-header` |

**Expression Policy** (Customization → Policies → Create → *Expression*,
name `vlr-revoke-when`): срабатывает только на деактивацию/удаление юзера:

```python
event = request.context.get("event")
if not event or event.action not in ("model_updated", "model_deleted"):
    return False
model = event.context.get("model", {})
if model.get("app") != "authentik_core" or model.get("model_name") != "user":
    return False
if event.action == "model_deleted":
    return True
from authentik.core.models import User
user = User.objects.filter(pk=model.get("pk")).first()
return bool(user and not user.is_active)
```

**Notification Rule** (Events → Notification Rules → Create, name `vlr-revoke`):
Transport = `vlr-webhook`, Group = пусто (вебхук-only), и **привяжите политику**
`vlr-revoke-when` к этому правилу (Bind existing policy).

> URL вебхука бьёт в узел vlr по приватной сети (NetBird/WG). Секрет в заголовке
> защищает эндпоинт; наружу `:9777` не открывать.

### 5. (Опционально) reconcile-backstop

На случай пропущенного вебхука (узел лежал) можно включить **редкий** опросный
reconcile. По умолчанию выключен. Нужен сервис-аккаунт с read-доступом к юзерам:

1. **Directory → Users → Create Service account** `svc-vlr-reconcile`, дать право
   `core.view_user`.
2. **Directory → Tokens → Create** (Intent: API) → это `authentik.token`.
3. В конфиг добавить `api_url`, `token` и **крупный** `reconcile_seconds`
   (например `86400` — раз в сутки), либо `vlr init ... --authentik-url <...>
   --authentik-token <...> --authentik-reconcile-seconds 86400`.

Fail-safe: при ошибке API или пустом списке активных reconcile ничего не удаляет.

## Проверка

```bash
# без заголовка — 403 (портал не отдаёт страницу без личности)
curl -si http://VLR_NODE_ADDR:9777/me | head -1        # HTTP/1.1 403

# с заголовком (как это делает Caddy после Authentik) — 200 + персональная ссылка
curl -s http://VLR_NODE_ADDR:9777/me \
  -H 'X-Authentik-Email: i.ivanov@genomed.ru' | grep -o 'base64/[a-f0-9]*'

# webhook отзыва (имитируем то, что шлёт Authentik) — юзер удаляется
curl -si -XPOST http://VLR_NODE_ADDR:9777/v1/authentik/event \
  -H 'Authorization: Bearer <webhook_secret>' \
  -d '{"action":"model_updated","email":"i.ivanov@genomed.ru","is_active":false}'
# -> {"ok":true,"revoked":true}   (revoked:false если юзера нет или он не из портала)
```

Через браузер: залогиньтесь в Authentik, откройте плитку «Личный VPN» — должна
открыться страница с вашей ссылкой. Первый вход **создаёт** юзера на узле (видно в
`vlr user list`). Деактивируйте себя в Authentik — вебхук удалит юзера.

## Выдача и отзыв

| Действие | Как |
|---|---|
| Выдать доступ | Ничего: первый вход сотрудника через плитку создаёт юзера сам |
| **Забрать доступ** | **Выключить учётку в Authentik** — вебхук мгновенно удалит юзера с узла (Xray перечитается), VLESS-ключ умрёт |
| Отозвать утёкшую ссылку (юзер остаётся) | `vlr user rotate <email>` — старый `/base64/<token>` мгновенно мёртв |
| Удалить/отозвать вручную | `vlr user rm <email>` на узле |

Отзыв (вебхук и backstop) трогает **только** portal-юзеров (`source: authentik`).
Заведённых вручную/по API (tg-клиенты и т.п.) — никогда не удаляет.

> Эндпоинт всегда отвечает `200` на корректный (даже если юзер не найден), чтобы
> Authentik не устраивал retry-шторм. Отказ — только на неверном секрете (`401`).

## Грабли

- **Outpost не знает provider** → forward_auth 403 на всё. Добавьте provider в
  Embedded Outpost (шаг 1).
- **Два хоста.** Authentik на EDGE, портал vlr на RU-узле. Caddy на EDGE должен
  дотягиваться до `VLR_NODE_ADDR:9777` по приватной сети. Публично `:9777` не
  открывать никогда.
- **`meta_launch_url` и токен.** Не пытайтесь вставить токен в launch URL через
  `%(attributes.vlr_token)s` — не работает (только top-level поля). Токен отдаёт
  страница `/me`, это by design.
- **Email в vlr != email в Authentik.** Портал ищет юзера по email из заголовка.
  Если сотрудник уже был заведён вручную с другим email — будет создан второй
  юзер. Держите email каноничным (рабочая почта).
- **Вебхук не доходит.** Проверьте: Notification Rule включён и к нему привязана
  политика `vlr-revoke-when`; Transport указывает на приватный URL узла; секрет в
  Header-mapping совпадает с `webhook_secret`. Тестовое событие — деактивируйте
  тестового юзера и смотрите `docker compose logs worker` (Authentik) + логи vlr.
- **`event.context` без `model`.** Убедитесь, что событие именно `model_updated`/
  `model_deleted` по User. Другие события (login и т.п.) политика отсекает.
- **Backstop не нужен, но включён.** Если `reconcile_seconds > 0` без реальной
  необходимости — это лишний опрос. Для чистого event-driven оставьте его `0`.
