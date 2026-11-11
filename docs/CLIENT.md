# Клиент: установка и использование

Один бинарник `cslvpn` содержит и сервер, и клиент. Клиенту нужны права на
создание TUN-интерфейса (root/администратор или `CAP_NET_ADMIN`).

## 1. Установка

Проще всего — штатным пакетом из релиза (workflow `packages` собирает их на
каждый push):

| Платформа | Файл | Команда |
|---|---|---|
| Debian/Ubuntu | `cslvpn_<ver>_{amd64,arm64}.deb` | `sudo dpkg -i cslvpn_*.deb` |
| RHEL/Fedora | `cslvpn-<ver>-1.*.rpm` | `sudo rpm -Uvh cslvpn-*.rpm` |
| Alpine | `cslvpn_<ver>_*.apk` | `sudo apk add --allow-untrusted cslvpn_*.apk` |
| Windows | `cslvpn-<ver>-windows-amd64.msi` | `msiexec /i cslvpn-*.msi` |
| Windows (exe) | `cslvpn-<ver>-windows-amd64-setup.exe` | двойной клик |
| macOS | `cslvpn-<ver>-macos-<arch>.pkg` | `sudo installer -pkg cslvpn-*.pkg -target /` |
| Любая Linux | `cslvpn-<ver>-linux-<arch>.tar.gz` | `tar -xzf …` |

Пакет даёт `/usr/bin/cslvpn` (Windows: `C:\Program Files\CSLVPN\cslvpn.exe`,
macOS: `/usr/local/bin/cslvpn`), юнит `cslvpn-client.service` и образец
конфига `client.env`. Клиент по-прежнему нужно запускать с правами на TUN:
юнит делает это через `User=root` + `CapabilityBoundingSet=CAP_NET_ADMIN
CAP_NET_RAW`.

```bash
sudo cp /etc/cslvpn/client.env.example /etc/cslvpn/client.env  # сервер, сеть, пароль, транспорт
sudo systemctl enable --now cslvpn-client.service
journalctl -u cslvpn-client -f
```

Без пакетного менеджера: бинарники лежат в релизах и в артефактах сборки
(вкладка **Actions**), либо `go build -o cslvpn ./cmd/cslvpn` (Go 1.24+).

## 2. Быстрый старт: сеть из трёх машин

Машина 1 (создаёт сеть):

```bash
sudo cslvpn client -server 203.0.113.10:7777 \
  -network office -password s3cret -create -transport udp -v
```

Машина 2 (входит в сеть, транспорт TCP):

```bash
sudo cslvpn client -server 203.0.113.10:7777 \
  -network office -password s3cret -transport tcp -v
```

Что произойдёт: клиент 1 создаст сеть `office` (`26.0.0.0/24`) и получит
`26.0.0.2`, клиент 2 получит `26.0.0.3`. Между ними — обычная IP-связность:

```bash
ping 26.0.0.3
curl http://26.0.0.3:8080/
ssh user@26.0.0.2
```

В логе клиента:

```
connected to 203.0.113.10:7777 via udp: network "office", member "m1", address 26.0.0.2/24, MTU 1400
READY: your address in "office" is 26.0.0.2 (transport udp)
peer "m2" at 26.0.0.3 via tcp
```

## 3. Параметры клиента

| Флаг | По умолчанию | Описание |
|---|---|---|
| `-server` | `$CSLVPN_SERVER` | адрес ретранслятора `host:port` |
| `-network` | — | имя сети (создаётся при `-create`) |
| `-password` | `$CSLVPN_PASSWORD` | пароль сети |
| `-transport` | `udp` | `udp` или `tcp` |
| `-create` | false | создать сеть, если её ещё нет |
| `-name` | имя хоста | имя участника в сети |
| `-ip` | авто | запросить конкретный адрес, например `26.0.0.10` |
| `-iface` | `cslvpn0` | имя TUN-интерфейса |
| `-mtu` | 1400 | MTU туннеля |
| `-internet` | false | весь трафик через ретранслятор (нужен `-gateway` на сервере) |
| `-v` | false | печатать изменения списка участников |
| `-debug` | false | печатать каждый пакет |

Пароль и адрес сервера можно не писать в командной строке (она видна в `ps`):

```bash
export CSLVPN_SERVER=203.0.113.10:7777
export CSLVPN_PASSWORD=s3cret
sudo -E cslvpn client -network office -transport tcp
```

## 4. Выбор транспорта UDP или TCP

| | UDP (по умолчанию) | TCP |
|---|---|---|
| Задержка | минимальная | чуть выше (head-of-line) |
| Потери | возможны, их разбирают протоколы внутри | нет |
| Плохие сети (гостевой Wi-Fi, корпоративный фаервол, CGNAT) | иногда блокируется | обычно проходит |
| Нагрузка на сервер | ниже | выше (состояние соединения) |

Переключается одной строкой: `-transport tcp`. В одной сети могут быть
участники с разными транспортами — сервер пересылает кадры между ними
прозрачно. Список участников с их транспортами виден в `-v` и в админ-API.

Если UDP «не работает» (ping по виртуальному адресу идёт, а приложения
висят) — почти всегда виноват фаервол на пути: попробуйте `-transport tcp`.

## 5. Проверка, что всё работает

```bash
ip addr show cslvpn0          # адрес 26.x.y.z/24
ip route                      # маршрут 26.x.y.0/24 dev cslvpn0
ping <адрес другого участника>
ss -lunp | grep cslvpn        # видно сокет к ретранслятору
sudo cslvpn client ... -debug # каждый пакет
```

## 6. Автозапуск

systemd (Linux):

```ini
# /etc/systemd/system/cslvpn-client.service
[Unit]
Description=CSLVPN client
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=CSLVPN_SERVER=203.0.113.10:7777
Environment=CSLVPN_PASSWORD=s3cret
ExecStart=/usr/local/bin/cslvpn client -network office -transport tcp
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now cslvpn-client
```

Windows: см. [WINDOWS.md](WINDOWS.md) (служба + Планировщик задач).

## 7. Частые проблемы

| Симптом | Причина и решение |
|---|---|
| `relay rejected the client: wrong network password` | пароль не совпадает (регистр важен) |
| `network not found (...)` | сети ещё нет: на первой машине нужен `-create`; клиент повторяет попытки сам |
| `transport mismatch` в логе сервера | клиент указал не тот транспорт, которым реально подключается (обычно прокси/балансировщик превращает UDP в TCP) |
| `tun: open /dev/net/tun: no such file` | нет модуля `tun`: `sudo modprobe tun` или включите в ядре |
| `operation not permitted` при создании TUN | запускать от root / с `CAP_NET_ADMIN` |
| Ping по 26.x идёт, TCP — нет | MTU/фрагментация на пути: уменьшите `-mtu` (например, 1280) |
| Адрес меняется после переподключения | имя участника занято другой сессией; сервер переиспользует адрес при том же имени, но после `-idle-network` сеть могла быть удалена |
| Долгий ping в `-internet` | трафик идёт через ретранслятор: это relay-режим, прямого P2P пока нет (см. ROADMAP) |
