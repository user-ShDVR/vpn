# API Flow — СвязьOK

Base URL: `https://api.yourdomain.com/api/v1`

Все protected-эндпоинты требуют заголовок:
```
Authorization: Bearer <token>
```

---

## 1. Регистрация

```
POST /auth/register
{
  "email": "user@example.com",
  "password": "mypassword",
  "referral_code": "a1b2c3d4"   // опционально
}
```

Ответ:
```json
{
  "token": "eyJhbG...",
  "user": { "id": "uuid", "email": "user@example.com", "role": "user" }
}
```

Если передан `referral_code` — оба (реферер и новый юзер) получают +5 дней к подписке.

Токен действует 7 дней. Сохраняем его — дальше используем везде.

---

## 2. Авто-провижн (получить VPN сразу после регистрации)

Это основной эндпоинт для клиентского приложения. Вызываем сразу после регистрации/логина.
Он делает всё за один запрос:
- Если нет подписки — создаёт бесплатную (Free, 30 дней, 200 ГБ)
- Если нет VPN-клиента на сервере — создаёт через 3x-ui
- Возвращает VLESS URI для подключения

```
POST /provision
Authorization: Bearer <token>
```

Ответ:
```json
{
  "status": "provisioned",
  "subscription": {
    "id": "uuid",
    "plan_id": "uuid",
    "starts_at": "2026-03-18T...",
    "expires_at": "2026-04-17T...",
    "is_active": true
  },
  "vless_uri": "vless://uuid@server:443?security=reality&..."
}
```

Если уже есть подписка и конфиг — вернёт `"status": "ok"` и текущий URI.

---

## 3. Подключение к VPN

Полученный `vless_uri` импортируется в клиент:
- **Android**: v2rayNG / Streisand — вставить URI
- **iOS**: Streisand / sing-box — вставить URI
- Или использовать subscription link (шаг 3а)

### 3а. Subscription URL (для готовых клиентов: Happ, V2RayTun, Hiddify)

```
GET /subscription/url
Authorization: Bearer <token>
```

Ответ:
```json
{ "url": "https://api.example.com/sub/<48hex>", "qr_png_base64": "..." }
```

URL — публичный endpoint `/sub/:token` (без JWT, rate-limited 30 req/min). Юзер
импортирует его в любой готовый клиент — поддерживаются стандартные заголовки
`Subscription-Userinfo`, `Profile-Update-Interval`, `Profile-Title`.

Ротация токена (старый URL умирает):
```
POST /subscription/rotate
Authorization: Bearer <token>
```

Legacy `/config` и `/subscription-link` удалены — используйте `/subscription/url`
+ `/sub/:token`.

---

## 4. Проверка статуса подписки

```
GET /subscription
Authorization: Bearer <token>
```

Ответ:
```json
{
  "subscription": {
    "id": "uuid",
    "plan_id": "uuid",
    "starts_at": "...",
    "expires_at": "...",
    "is_active": true
  }
}
```

Если подписки нет: `{ "subscription": null }`

---

## 5. Проверка трафика

```
GET /traffic
Authorization: Bearer <token>
```

Ответ (байты):
```json
{
  "up": 123456789,
  "down": 987654321,
  "total": 1111111110
}
```

---

## 6. Активация другого плана

Когда юзер хочет перейти на платный план (после оплаты):

```
POST /subscription/activate
Authorization: Bearer <token>
{
  "plan_id": "uuid-of-paid-plan"
}
```

Старая подписка автоматически деактивируется. Создаётся новая + новый VPN-клиент на сервере.

Ответ:
```json
{
  "subscription": { ... },
  "vless_uri": "vless://new-uuid@..."
}
```

---

## 7. Получить текущий конфиг

Используйте `/subscription/url` (см. шаг 3а) — он возвращает публичную ссылку
вида `https://api.example.com/sub/<token>`, которую готовые клиенты могут
ре-импортировать в любой момент. Старый `/config` удалён.

---

## 8. Реферальная система

### Получить свой реферальный код

```
GET /referral
Authorization: Bearer <token>
```

Ответ:
```json
{ "referral_code": "a1b2c3d4", "referral_count": 3 }
```

Код генерируется автоматически при первом запросе.

### Применить чужой код (если не передал при регистрации)

```
POST /referral/apply
Authorization: Bearer <token>
{ "code": "a1b2c3d4" }
```

Ответ:
```json
{ "message": "Both you and the referrer received 5 bonus days", "bonus_days": 5 }
```

Применить можно только один раз. Нельзя использовать свой код.

---

## 9. Профиль и безопасность

### Текущий юзер

```
GET /auth/me
Authorization: Bearer <token>
```

### Смена пароля

```
PUT /auth/password
Authorization: Bearer <token>
{
  "current_password": "old",
  "new_password": "newpassword"
}
```

### Повторный логин

```
POST /auth/login
{ "email": "user@example.com", "password": "mypassword" }
```

---

## 10. Список планов (публичный)

```
GET /plans
```

Ответ:
```json
[
  { "id": "uuid", "name": "Free", "duration_days": 30, "traffic_limit_gb": 200, "max_devices": 5 },
  { "id": "uuid", "name": "Pro", "duration_days": 30, "traffic_limit_gb": null, "max_devices": 10 }
]
```

---

## Типичный flow клиентского приложения

```
Первый запуск:
  register → provision → сохранить vless_uri → подключиться

Повторный запуск:
  login → GET /subscription/url → импортировать в клиент

На главном экране:
  GET /subscription   — показать "до какого числа"
  GET /traffic        — показать "сколько потрачено"

Настройки:
  GET /referral       — показать код и счётчик
  GET /plans          — показать доступные планы
  PUT /auth/password  — сменить пароль
```

---

## Admin endpoints

Требуют `role: "admin"` в JWT.

| Метод | Путь | Описание |
|-------|------|----------|
| `GET` | `/admin/users?limit=50&offset=0` | Список юзеров с пагинацией |
| `GET` | `/admin/servers` | Список серверов (credentials скрыты) |
| `GET` | `/admin/stats` | Кол-во юзеров, активных подписок, серверов |
| `POST` | `/admin/plans` | Создать план |
| `POST` | `/admin/subscription/grant` | Выдать подписку юзеру |
| `POST` | `/admin/servers` | Добавить сервер связи |

### Создать план
```json
POST /admin/plans
{ "name": "Pro", "duration_days": 30, "traffic_limit_gb": null, "max_devices": 10 }
```

### Выдать подписку вручную
```json
POST /admin/subscription/grant
{ "user_id": "uuid", "plan_id": "uuid" }
```

### Добавить сервер
```json
POST /admin/servers
{
  "name": "RU-1",
  "panel_url": "https://server-ip:2053",
  "panel_user": "admin",
  "panel_pass": "secret",
  "inbound_id": 1,
  "type": "entry",
  "host": "1.2.3.4",
  "port": 443,
  "sub_url": "https://server-ip:2096",
  "sub_path": "/mysecret/sub/"
}
```

---

## Rate limits

- `/auth/register`, `/auth/login` — 10 запросов/мин на IP
- Остальные эндпоинты — без ограничений (ограничены только авторизацией)
