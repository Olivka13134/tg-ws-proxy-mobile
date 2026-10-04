package app

var strings_ = map[string]map[string]string{
	"ru": {
		"title":         "TG WS Proxy",
		"running":       "Работает · %s:%d",
		"stopped":       "Остановлен",
		"stats":         "Соединений: %d · ↑ %s ↓ %s",
		"open_tg":       "Подключить в Telegram",
		"copy_link":     "Скопировать ссылку",
		"copied":        "Ссылка скопирована",
		"settings":      "Настройки…",
		"restart":       "Перезапустить прокси",
		"open_log":      "Открыть лог",
		"open_folder":   "Открыть папку с настройками",
		"autostart":     "Запускать вместе с Windows",
		"quit":          "Выход",
		"port_busy":     "Порт %d уже занят. Скорее всего, запущен старый TgWsProxy — закройте его (иконка в трее → Выход) и нажмите «Перезапустить прокси».",
		"already":       "TG WS Proxy уже запущен (иконка в трее).",
		"first_run":     "Прокси запущен.\n\nДобавить его в Telegram сейчас? Откроется Telegram с кнопкой «Подключить прокси».",
		"first_run_imp": "Прокси запущен. Настройки и secret перенесены из старого TgWsProxy, поэтому прокси, уже добавленный в Telegram, продолжит работать.\n\nЗакройте старый TgWsProxy, если он запущен.\n\nОткрыть Telegram, чтобы проверить подключение?",
		"error":         "Ошибка",
		"start_error":   "Не удалось запустить прокси:\n%s",
	},
	"en": {
		"title":         "TG WS Proxy",
		"running":       "Running · %s:%d",
		"stopped":       "Stopped",
		"stats":         "Connections: %d · ↑ %s ↓ %s",
		"open_tg":       "Connect in Telegram",
		"copy_link":     "Copy link",
		"copied":        "Link copied",
		"settings":      "Settings…",
		"restart":       "Restart proxy",
		"open_log":      "Open log",
		"open_folder":   "Open settings folder",
		"autostart":     "Start with Windows",
		"quit":          "Quit",
		"port_busy":     "Port %d is already in use. The old TgWsProxy is probably running: quit it (tray icon → Exit) and press \"Restart proxy\".",
		"already":       "TG WS Proxy is already running (see the tray).",
		"first_run":     "The proxy is running.\n\nAdd it to Telegram now? Telegram will open with a \"Connect proxy\" button.",
		"first_run_imp": "The proxy is running. Settings and secret were imported from the old TgWsProxy, so the proxy already added in Telegram keeps working.\n\nQuit the old TgWsProxy if it is running.\n\nOpen Telegram to check the connection?",
		"error":         "Error",
		"start_error":   "Could not start the proxy:\n%s",
	},
}

func T(lang, key string) string {
	if m, ok := strings_[lang]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	return strings_["en"][key]
}
