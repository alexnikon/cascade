'use strict';

/**
 * Russian translations for user-facing backend error messages.
 *
 * Cascade's Go backend (338 fiber.NewError call sites, 95 distinct messages)
 * returns English text, and api.js surfaces it verbatim in toasts. Rather than
 * teaching the Go side about locales, we map the English message here on the
 * client, right before it reaches the UI.
 *
 * Anything not listed falls back to the original English, so a missed or
 * newly-added backend message degrades gracefully instead of breaking.
 *
 * Deliberately NOT translated (internal/debug plumbing, never meaningful alone):
 *   '...', 'address: ', 'db error', 'db insert: ', 'encrypt: ', 'gzip close: ',
 *   'invalid gzip: ', 'open upload: ', 'read upload: ', 'tar close: ',
 *   'tmp file: ', 'write tmp: ', 'session error', 'session save error',
 *   'proxy build request: ', 'proxy read response: ', 'cannot read backup DB: '
 */

// Exact-match messages.
const exact = {
  'Failed to reset peer traffic': 'Не удалось сбросить статистику пира',
  'Invalid JSON body': 'Некорректное тело JSON',
  'TOTP is not enabled': 'TOTP не включён',
  'Template name is required': 'Укажите название шаблона',
  'Template not found': 'Шаблон не найден',
  'admin only': 'Доступно только администратору',
  'adminDown must be a boolean': 'adminDown должен быть булевым значением',
  'alias not found': 'Алиас не найден',
  'auth not initialised': 'Аутентификация не инициализирована',
  'capture not found or already downloaded': 'Захват не найден или уже скачан',
  'conf is required': 'Требуется conf',
  'config generation failed': 'Не удалось сгенерировать конфигурацию',
  'current password is incorrect': 'Текущий пароль указан неверно',
  'current password is required to change a password': 'Для смены пароля укажите текущий пароль',
  'current password is required to change your password': 'Для смены своего пароля укажите текущий пароль',
  "defaultFwPolicy: must be 'accept' or 'drop'": "defaultFwPolicy: должно быть 'accept' или 'drop'",
  'domain resolver is not running': 'Резолвер доменов не запущен',
  'export-json is only available for interconnect peers': 'export-json доступен только для interconnect-пиров',
  'failed to create temp file': 'Не удалось создать временный файл',
  'failed to generate QR code': 'Не удалось сгенерировать QR-код',
  'failed to generate TOTP key': 'Не удалось сгенерировать ключ TOTP',
  'failed to generate link token': 'Не удалось сгенерировать токен ссылки',
  'failed to persist metrics settings': 'Не удалось сохранить настройки метрик',
  'failed to write temp file': 'Не удалось записать временный файл',
  'file is empty': 'Файл пуст',
  'forbidden: you can only delete your own account': 'Запрещено: можно удалить только свою учётную запись',
  'forbidden: you can only update your own account': 'Запрещено: можно изменить только свою учётную запись',
  'gateway group not found': 'Группа шлюзов не найдена',
  'gateway not found': 'Шлюз не найден',
  'host is required': 'Укажите host',
  'ids must not be empty': 'Список ids не может быть пустым',
  'iface is required': 'Укажите iface',
  'interface not found': 'Интерфейс не найден',
  'invalid JSON': 'Некорректный JSON',
  'invalid JSON body': 'Некорректное тело JSON',
  'invalid JSON body: expected { text: string }': 'Некорректное тело JSON: ожидается { text: string }',
  'invalid backup: missing peers array': 'Некорректная резервная копия: отсутствует массив peers',
  'invalid expireDate: expected RFC3339 or YYYY-MM-DD': 'Некорректный expireDate: ожидается RFC3339 или YYYY-MM-DD',
  'invalid file id': 'Некорректный идентификатор файла',
  'invalid host': 'Некорректный host',
  'invalid multipart form': 'Некорректная multipart-форма',
  'invalid page': 'Некорректная страница',
  'invalid period': 'Некорректный период',
  'invalid token': 'Некорректный токен',
  'ip query parameter is required': 'Требуется query-параметр ip',
  'job not found': 'Задание не найдено',
  'json is required': 'Требуется json',
  'key is required': 'Укажите ключ',
  'key must start with gateway:': "Ключ должен начинаться с 'gateway:'",
  'listenPort is required': 'Укажите listenPort',
  'mark must be an integer': 'mark должен быть целым числом',
  'name and url are required': 'Укажите name и url',
  'name is required': 'Укажите название',
  'no TOTP setup in progress': 'Настройка TOTP не запущена',
  'no TOTP setup in progress — call /setup first': 'Настройка TOTP не запущена — сначала вызовите /setup',
  'no valid CIDR entries found in file': 'В файле не найдено корректных записей CIDR',
  'not authenticated': 'Не выполнен вход',
  'not ready': 'Ещё не готово',
  'peer not found': 'Пир не найден',
  'protocolVersion must be 2.0 or 3.1': 'protocolVersion должен быть 2.0 или 3.1',
  "provide backup file in 'backup' field": "Передайте файл резервной копии в поле 'backup'",
  "provide one or more files in 'configs' field": "Передайте один или несколько файлов в поле 'configs'",
  'refresh is only supported for domain aliases': 'Обновление поддерживается только для доменных алиасов',
  'remote not found': 'Удалённый сервер не найден',
  'settings must be an object': 'settings должен быть объектом',
  'token not found or already used': 'Токен не найден или уже использован',
  'too many ids': 'Слишком много ids',
  'user not found': 'Пользователь не найден',
  'username and password are required': 'Укажите имя пользователя и пароль',
};

// Messages the backend formats as "<prefix>: <details>" (Go's fmt.Errorf("...: %w")).
// The client only sees the joined string, so match on the prefix and keep the details.
const prefixes = {
  'could not find free port': 'Не удалось найти свободный порт',
  'iperf3 not found or failed to start': 'iperf3 не найден или не запустился',
  'qr generation failed': 'Не удалось сгенерировать QR-код',
  'proxy request failed': 'Не удалось выполнить прокси-запрос',
  'build AmneziaWG params': 'Не удалось собрать параметры AmneziaWG',
  'invalid settings': 'Некорректные настройки',
  'invalid ifaceMap JSON': 'Некорректный JSON ifaceMap',
};

/**
 * Translate a backend error message into Russian.
 * Falls back to the original string when there is no translation.
 *
 * @param {string} message
 * @returns {string}
 */
export function translateApiError(message) {
  if (typeof message !== 'string' || message === '') {
    return message;
  }

  if (Object.prototype.hasOwnProperty.call(exact, message)) {
    return exact[message];
  }

  for (const prefix of Object.keys(prefixes)) {
    if (message.startsWith(prefix + ': ')) {
      return prefixes[prefix] + ': ' + message.slice(prefix.length + 2);
    }
  }

  return message;
}
