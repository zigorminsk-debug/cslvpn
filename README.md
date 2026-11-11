# CSLVPN

Виртуальные локальные сети (VLAN) поверх интернета — как Radmin VPN, но с
открытым протоколом, собственным сервером-ретранслятором на белом IP и **явным
выбором транспорта UDP или TCP**.

Один статический бинарник без зависимостей: сервер-ретранслятор, клиент и CLI
управления.

```bash
# сервер на хосте с публичным IP
sudo cslvpn server -listen :7777 -admin 127.0.0.1:7778

# машина 1: создать сеть "office"
sudo cslvpn client -server 203.0.113.10:7777 -network office -password s3cret -create

# машина 2: войти в ту же сеть по TCP
sudo cslvpn client -server 203.0.113.10:7777 -network office -password s3cret -transport tcp
```

После подключения машины видят друг друга по адресам `26.x.y.z` — как в одной
локальной сети: ping, SMB, RDP, игры, HTTP, SSH.

## Скачать готовые сборки

Релизы с бинарниками (Windows/Linux/macOS, amd64/arm64/armv7) и контрольными
суммами: [Releases](https://github.com/zigorminsk-debug/cslvpn/releases).

```bash
# Linux x86_64
curl -LO https://github.com/zigorminsk-debug/cslvpn/releases/latest/download/cslvpn-linux-amd64
curl -LO https://github.com/zigorminsk-debug/cslvpn/releases/latest/download/SHA256SUMS-linux-amd64.txt
sha256sum -c SHA256SUMS-linux-amd64.txt
chmod +x cslvpn-linux-amd64 && sudo mv cslvpn-linux-amd64 /usr/local/bin/cslvpn
```

## Что умеет

* **Изолированные сети по имени и паролю.** Разные сети не видят друг друга
  никогда: сервер пересылает кадры только внутри `/24` своей сети.
* **Выбор транспорта UDP или TCP** (`-transport udp|tcp`). Оба слушателя на
  одном порту; клиент указывает транспорт в рукопожатии, сервер проверяет его
  и подтверждает в `WELCOME`. Участники одной сети могут использовать разные
  транспорты.
* **Шифрование**: AES-256-GCM на каждый кадр, ключи из эфемерного X25519 +
  PBKDF2-SHA256 (пароль сети никогда не передаётся), взаимная аутентификация.
* **Работа за NAT/CGNAT** без проброса портов: клиенту нужен только исходящий
  доступ.
* **Интернет через сервер** (опционально, `-internet` + `-gateway`): default
  route в туннель, NAT на сервере, изоляция сетей сохраняется.
* **Управление**: админ-API (статистика, предсоздание сетей, kick с баном),
  лимиты участников/сетей, автопереподключение.
* **Кроссплатформенность**: Windows, Linux, macOS (amd64/arm64/armv7), сборка
  через GitHub Actions.

Подробное сравнение с Radmin VPN — в [docs/RADMIN-VPN-ANALYSIS.md](docs/RADMIN-VPN-ANALYSIS.md).

## Документация

| Документ | Содержание |
|---|---|
| [docs/RADMIN-VPN-ANALYSIS.md](docs/RADMIN-VPN-ANALYSIS.md) | разбор архитектуры Radmin VPN и что/как воспроизводит CSLVPN |
| [docs/PROTOCOL.md](docs/PROTOCOL.md) | проводной протокол v1 (кадры, рукопожатие, ключи, ошибки) |
| [docs/SERVER.md](docs/SERVER.md) | развёртывание ретранслятора, systemd, Docker, режим шлюза, мониторинг |
| [docs/CLIENT.md](docs/CLIENT.md) | установка и использование клиента, диагностика |
| [docs/WINDOWS.md](docs/WINDOWS.md) | особенности Windows (адаптер, Wintun, служба) |
| [docs/ROADMAP.md](docs/ROADMAP.md) | что сделано и что дальше (P2P, L2, GUI) |
| [docs/DEVELOPMENT-LOG.md](docs/DEVELOPMENT-LOG.md) | журнал разработки: решения, найденные ошибки, как проверялось |

## Сборка и тесты

```bash
go build -o cslvpn ./cmd/cslvpn      # Go 1.24+, без внешних зависимостей
go test ./...                        # юнит + интеграционные (реальный ретранслятор)
sudo bash scripts/e2e-test.sh        # сети, UDP+TCP, изоляция, админ-API (netns + tun)
sudo bash scripts/nat-test.sh        # интернет через ретранслятор (gateway + NAT)
```

CI (`.github/workflows/build.yml`) на каждый push: `gofmt`, `go vet`,
`go test`, race-детектор, e2e в network namespaces (сети + UDP/TCP + изоляция +
NAT), кросс-сборка под 7 платформ. Пуш тега `v*` публикует релиз с бинарниками
и `SHA256SUMS`; пуш в `main` — ночной (prerelease) релиз.

## Архитектура

```
        клиент A (26.0.0.2, udp) ─┐
                                   ├─►  ретранслятор :7777 (UDP+TCP)  ──► интернет (опц. NAT)
        клиент B (26.0.0.3, tcp) ──┘        реестр сетей + изоляция
```

* `internal/proto` — кадры и сообщения (без зависимостей);
* `internal/crypto` — ключи, AEAD, окно повторов;
* `internal/relay` — ретранслятор: сессии, сети, UDP/TCP листенеры, админ-API;
* `internal/client` — клиент: рукопожатие, туннель, реконнект;
* `internal/tun` — TUN-устройство и настройка сети (Linux, заготовка Windows);
* `cmd/cslvpn` — CLI (`server`, `client`, `status`).

## Ограничения

Сейчас весь трафик идёт через ретранслятор: прямого P2P-канала (hole punching)
пока нет, поэтому задержка определяется расстоянием до сервера. Это осознанный
выбор в пользу предсказуемой работы за любым NAT; P2P — следующий шаг
([docs/ROADMAP.md](docs/ROADMAP.md)). Broadcast-зависимые игры (L2) и
Windows-адаптер «из коробки» тоже в дорожной карте.

## Лицензия

MIT (см. LICENSE).
