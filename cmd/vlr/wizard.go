package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// isInteractive reports whether stdin is a real terminal (so we may prompt).
// Uses only the stdlib: a char device on stdin means an interactive TTY.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

var stdinReader = bufio.NewReader(os.Stdin)

// ask prints a label and returns the trimmed line. def is shown and returned on
// an empty answer.
func ask(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := stdinReader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// runInitWizard fills the init parameters interactively. It returns the role and
// mutates the provided pointers for the values the chosen role needs.
func runInitWizard(role, nodeID, host, region, mainURL, apiListen, token, pullBearer *string) {
	fmt.Print(`
========================================
   vlr — настройка узла
========================================

Выберите режим узла:

  1) Самостоятельный (standalone)
     Узел делает всё сам: терминирует VLESS+Reality, держит каскад RU→EU,
     хранит пользователей и метрики локально, сам отдаёт base64-подписку.
     Ничего наружу не шлёт. Для одиночного узла.

  2) Дочерний (child)
     Тот же VPN, но отчитывается на главный (main) сервер: шлёт лёгкий
     heartbeat и отдаёт статистику по запросу. Управление — на main.

  3) Главный / управляющий (main)
     Без VPN. Принимает heartbeat от дочерних узлов, делает delta-pull,
     собирает статистику и выдаёт подписки централизованно.

`)
	switch ask("Режим 1-3", "1") {
	case "2":
		*role = "child"
	case "3":
		*role = "main"
	default:
		*role = "standalone"
	}

	*nodeID = ask("ID узла (например ru-yc-msk-01)", *nodeID)
	for *nodeID == "" {
		*nodeID = ask("ID узла обязателен, введите", "")
	}

	switch *role {
	case "main":
		*apiListen = ask("Адрес API (host:port)", "0.0.0.0:8443")
	case "standalone", "child":
		*region = ask("Регион (метка, напр. RU/Yandex)", *region)
		*host = ask("Публичный адрес (IP/домен), пусто = автоопределение", "")
		if *role == "child" {
			*mainURL = ask("URL main-сервера (напр. https://main:8443/v1)", *mainURL)
			*token = ask("Токен узла для heartbeat", "")
			*pullBearer = ask("Bearer для pull (main → этот узел)", "")
		}
	}
	fmt.Println()
}
