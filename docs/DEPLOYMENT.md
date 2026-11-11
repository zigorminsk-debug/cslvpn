# Развёртывание системы «с нуля»

Пошаговая инструкция: от пустого VPS до рабочей изолированной сети, в которую
входят машины на Windows, Linux и macOS. Если нужен только один раздел —
ниже есть оглавление; полные описания флагов и протокола лежат в
[SERVER.md](SERVER.md), [CLIENT.md](CLIENT.md), [WINDOWS.md](WINDOWS.md),
[PROTOCOL.md](PROTOCOL.md).

```
   ┌─────────────┐   UDP/TCP 7777   ┌──────────────────┐   UDP/TCP 7777   ┌─────────────┐
   │ клиент A    │◄────────────────►│ ретранслятор     │◄────────────────►│ клиент B    │
   │ 26.0.0.2/24 │  шифрованный канал│ белый IP :7777   │                  │ 26.0.0.3/24 │
   └─────────────┘                  │ + админ-API 7778 │                  └─────────────┘
       сеть "office" ───────────────┘    изоляция сетей: 26.0.0.0/24 vs 26.0.1.0/24
```

Оглавление:

1. [Что нужно](#1-что-нужно)
2. [Сервер: подготовка VPS](#2-сервер-подготовка-vps)
3. [Установка ретранслятора](#3-установка-ретранслятора)
4. [Первый запуск и проверка](#4-первый-запуск-и-проверка)
5. [Служба и автозапуск](#5-служба-и-автозапуск)
6. [Клиенты: Linux/macOS](#6-клиенты-linuxmacos)
7. [Клиенты: Windows](#7-клиенты-windows)
8. [Проверка изоляции сетей](#8-проверка-изоляции-сетей)
9. [Интернет через сервер (опционально)](#9-интернет-через-сервер-опционально)
10. [Эксплуатация: логи, статистика, обновление](#10-эксплуатация-логи-статистика-обновление)
11. [Диагностика проблем](#11-диагностика-проблем)
12. [Чек-лист готовности](#12-чек-лист-готовности)

---

## 1. Что нужно

| Роль | Требования |
|---|---|
| Сервер | VPS с **белым IPv4**, Linux x86_64/arm64 (Debian 12/Ubuntu 22.04+ проверены), 1 vCPU, 512 МБ RAM, root/sudo |
| Порты | `UDP/7777` **и** `TCP/7777` (достаточно одного, если нужен только один транспорт) |
| Админ-API | `127.0.0.1:7778` — наружу **не** открывать |
| Клиенты | Windows 10/11, Linux (Debian/Ubuntu/Alpine/Fedora), macOS 12+; права администратора для TUN |
| Сеть клиента | любой NAT/CGNAT, нужен только исходящий доступ на `IP:7777` |

Пропускная способность: ~20–50 МБ RAM на 100 сессий, CPU почти не тратится
(AES-GCM с AES-NI). Канал — сколько даёт VPS.

## 2. Сервер: подготовка VPS

```bash
# обновление и базовые утилиты
sudo apt update && sudo apt -y upgrade
sudo apt -y install curl ca-certificates iproute2 iptables

# время должно быть точным (таймстемпы рукопожатий)
sudo timedatectl set-ntp true

# порты (ufw; если firewall нет — шаг пропустить)
sudo ufw allow 22/tcp
sudo ufw allow 7777/udp
sudo ufw allow 7777/tcp
sudo ufw enable
```

Если VPS за облачным firewall/security group (AWS, GCP, Hetzner, Oracle) —
те же два правила надо добавить и там: `UDP 7777`, `TCP 7777`.

Проверка, что порты действительно открыты снаружи (с домашней машины):

```bash
nc -vz <IP-сервера> 7777      # TCP -> "succeeded"
nc -vzu <IP-сервера> 7777     # UDP -> без ответа, но и без "connection refused"
```

## 3. Установка ретранслятора

### Вариант А — пакет (рекомендуется)

```bash
# Debian/Ubuntu (amd64; для arm64 — cslvpn_<ver>_arm64.deb)
curl -LO https://github.com/zigorminsk-debug/cslvpn/releases/latest/download/cslvpn_0.2.0_amd64.deb
sudo dpkg -i cslvpn_0.2.0_amd64.deb

# RHEL/Fedora
sudo rpm -Uvh cslvpn-0.2.0-1.x86_64.rpm
# Alpine
sudo apk add --allow-untrusted cslvpn_0.2.0_x86_64.apk
```

Пакет ставит `/usr/bin/cslvpn`, юниты `cslvpn.service` /
`cslvpn-client.service`, образцы конфигов `/etc/cslvpn/*.example` (0640) и
документацию в `/usr/share/doc/cslvpn/`. Службы **не включаются**
автоматически — сначала конфиг.

### Вариант Б — бинарник

```bash
curl -LO https://github.com/zigorminsk-debug/cslvpn/releases/latest/download/cslvpn-linux-amd64
curl -LO https://github.com/zigorminsk-debug/cslvpn/releases/latest/download/SHA256SUMS-linux-amd64.txt
sha256sum -c SHA256SUMS-linux-amd64.txt
chmod +x cslvpn-linux-amd64 && sudo install -m755 cslvpn-linux-amd64 /usr/local/bin/cslvpn
cslvpn version
```

### Вариант В — Docker

```bash
docker build -t cslvpn .          # или взять готовый образ из своего registry
docker run -d --name cslvpn --restart unless-stopped --network host \
  cslvpn server -listen :7777 -admin 127.0.0.1:7778
```

`--network host` обязателен: иначе UDP и TCP не будут видны снаружи, а режим
шлюза (TUN + iptables) не заработает.

## 4. Первый запуск и проверка

Сначала — вручную, чтобы увидеть ошибки в консоли:

```bash
sudo cslvpn server -listen :7777 -admin 127.0.0.1:7778 -v
```

Где-то рядом:

```bash
curl -s http://127.0.0.1:7778/api/v1/health     # {"status":"ok", ...}
curl -s http://127.0.0.1:7778/api/v1/stats      # счётчики и список сетей
```

Создать сеть заранее (тогда `-allow-create` клиентам можно запретить):

```bash
curl -s -X POST http://127.0.0.1:7778/api/v1/networks \
  -H 'Content-Type: application/json' \
  -d '{"name":"office","password":"s3cret"}'
```

Через сам бинарник: `cslvpn status -admin http://127.0.0.1:7778`.

> Файрвол на сервере не нужен: ретранслятор не маршрутизирует трафик, пока не
> включён режим шлюза (§9). NAT на сервере тоже не требуется.

## 5. Служба и автозапуск

Из пакета (Debian/Ubuntu/Fedora/Alpine): postinstall уже создал
`/etc/cslvpn/server.env` из образца (режим 0600), остаётся его поправить.

```bash
sudoedit /etc/cslvpn/server.env        # порт, админ-адрес, токен
sudo systemctl enable --now cslvpn.service
sudo systemctl status cslvpn.service
```

Юнит передаёт в программу только `CSLVPN_LISTEN`, `CSLVPN_ADMIN` и читает
`CSLVPN_ADMIN_TOKEN` из окружения. Лимиты и таймауты (флаги `-max-members`,
`-max-networks`, `-idle-network`, `-ban`) задаются drop-in'ом, если нужны
нестандартные:

```bash
sudo systemctl edit cslvpn.service
# [Service]
# ExecStart=
# ExecStart=/usr/bin/cslvpn server -listen :7777 -admin 127.0.0.1:7778 -max-members 200
```

Без пакета — два файла вручную. `/etc/cslvpn/server.env`:

```ini
CSLVPN_LISTEN=:7777
CSLVPN_ADMIN=127.0.0.1:7778
CSLVPN_ADMIN_TOKEN=смените-этот-токен
CSLVPN_MAX_MEMBERS=150
```

`/etc/systemd/system/cslvpn.service`:

```ini
[Unit]
Description=CSLVPN relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cslvpn server -listen :7777 -admin 127.0.0.1:7778
EnvironmentFile=-/etc/cslvpn/server.env
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now cslvpn
```

Юнит из пакета строже (DynamicUser, `ProtectSystem=strict`, `PrivateTmp`,
`NoNewPrivileges`) — при ручной установке эти строки можно скопировать из
`/usr/lib/systemd/system/cslvpn.service`. Режим шлюза в этом юните требует
drop-in с `User=root`/`DynamicUser=no` (см. комментарий в
`/etc/cslvpn/server.env.example`).

## 6. Клиенты: Linux/macOS

```bash
# Linux
sudo dpkg -i cslvpn_0.2.0_amd64.deb        # или tar.gz / rpm / apk
# macOS
sudo installer -pkg cslvpn-0.2.0-macos-arm64.pkg -target /

# вход в сеть (создать её, если ещё нет)
sudo cslvpn client -server <IP-сервера>:7777 \
  -network office -password s3cret -create -transport udp -v
```

На второй машине — то же самое, но без `-create` и, при желании, с
`-transport tcp`:

```bash
sudo cslvpn client -server <IP-сервера>:7777 \
  -network office -password s3cret -transport tcp -v
```

В логе появится:

```
connected to <IP>:7777 via udp: network "office", member "m1", address 26.0.0.2/24, MTU 1400
```

Значит всё готово: `ping 26.0.0.3`, `ssh`, `curl`, SMB — обычная IP-связность.

Как службу (Linux-клиент): postinstall создал `client.env` из образца.

```bash
sudoedit /etc/cslvpn/client.env      # сервер, сеть, пароль, транспорт, iface
sudo systemctl enable --now cslvpn-client.service
journalctl -u cslvpn-client -f
```

macOS-клиент как демон: `io.cslvpn.relay.plist` ставится выключенным, для
клиента удобнее `launchctl` со своим plist или запуск из терминала.

## 7. Клиенты: Windows

```powershell
# MSI (тихая установка — /qn)
msiexec /i cslvpn-0.2.0-windows-amd64.msi
# либо setup.exe: двойной клик, или /S для тихой установки
```

Установщик кладёт `C:\Program Files\CSLVPN\cslvpn.exe`, добавляет PATH,
пункты меню «Пуск», правила брандмауэра для 7777/udp и 7777/tcp, а также
(опцией) службу `cslvpn-relay`.

```powershell
# клиент: нужны права администратора (TUN-адаптер)
cslvpn client -server <IP-сервера>:7777 -network office -password s3cret -transport tcp -v
```

Требуется Wintun (`wintun.dll` рядом с `cslvpn.exe`) — подробности и
ограничения в [WINDOWS.md](WINDOWS.md).

## 8. Проверка изоляции сетей

Главное отличие от «просто VPN»: сети не видят друг друга.

```bash
# на клиенте в сети "office"
ping 26.0.0.3            # участник той же сети -> ответ
# вторая сеть "home" получит 26.0.1.x
ping 26.0.1.2            # из "office" -> таймаут, трафик не пересылается
```

На сервере видно разделение:

```bash
cslvpn status -admin http://127.0.0.1:7778
# network "office"  subnet 26.0.0.0/24  members 2
# network "home"    subnet 26.0.1.0/24  members 1
```

Проверка «чужого» пароля: клиент с неверным паролем получает отказ
рукопожатия и не попадает в сеть (в логе сервера — `handshake failed`).

Управление участниками:

```bash
curl -s http://127.0.0.1:7778/api/v1/networks/office/members
curl -s -X DELETE http://127.0.0.1:7778/api/v1/networks/office/members/m2   # kick + бан 15m
```

## 9. Интернет через сервер (опционально)

Нужно, если клиенты должны выходить в интернет через сервер (как в обычном
VPN), сохраняя изоляцию сетей.

```bash
# 1. TUN-интерфейс шлюза
sudo ip tuntap add mode tun dev cslgw
sudo ip addr add 10.77.0.1/24 dev cslgw
sudo ip link set cslgw mtu 1400 up
sudo sysctl -w net.ipv4.ip_forward=1

# 2. сервер в режиме шлюза
sudo cslvpn server -listen :7777 -admin 127.0.0.1:7778 -gateway -gateway-iface cslgw
```

Сервер сам настроит MASQUERADE и FORWARD для `26.0.0.0/8`; для постоянной
работы добавьте `net.ipv4.ip_forward=1` в `/etc/sysctl.d/99-cslvpn.conf`.

Клиент: `sudo cslvpn client ... -internet` — default route уходит в туннель,
адрес ретранслятора закрепляется через прежний шлюз.

## 10. Эксплуатация: логи, статистика, обновление

```bash
journalctl -u cslvpn -f                 # логи службы
cslvpn status -admin http://127.0.0.1:7778
curl -s http://127.0.0.1:7778/api/v1/stats | jq .counters
```

Что смотреть: `sessions`, `networks`, `rx_frames`/`tx_frames`,
`dropped`, `handshakes`. Растущий `dropped` при жалобах на связь — включите
`-debug` и проверьте, доходят ли пакеты до сервера.

Обновление:

```bash
sudo dpkg -i cslvpn_<новая-версия>_amd64.deb   # существующие /etc/cslvpn/*.env не перезаписываются
sudo systemctl restart cslvpn
```

Удаление: `sudo dpkg -r cslvpn` — службы останавливаются и отключаются,
`/etc/cslvpn` сохраняется.

## 11. Диагностика проблем

| Симптом | Что проверить |
|---|---|
| клиент не подключается, таймаут | порт закрыт: `nc -vz <IP> 7777` снаружи; правила облачного firewall; `-listen` слушает нужный адрес |
| работает TCP, не работает UDP | UDP-правило в firewall/security group; провайдер режет UDP — используйте `-transport tcp` |
| `handshake failed` на сервере | неверный пароль или имя сети; сеть создана с другим паролем |
| клиент подключился, но ping не идёт | разные сети (проверьте подсети в `status`); MTU — попробуйте `-mtu 1280` |
| работает 5–10 минут, потом рвётся | NAT-таймаут: оставьте keepalive по умолчанию, проверьте `-debug` |
| `permission denied` при создании TUN | нужен root/администратор либо `CAP_NET_ADMIN` |
| Windows: адаптер не создаётся | нет `wintun.dll` рядом с `cslvpn.exe` (см. WINDOWS.md) |
| интернет-режим не работает | `ip_forward=1`, `-gateway-iface` существует, MASQUERADE в `iptables -t nat -L` |

Трафик туннеля в логи не пишется — только метаданные (кто, когда, откуда,
сколько). Содержимое кадров не журналируется никогда.

## 12. Чек-лист готовности

- [ ] у сервера белый IP, `UDP/7777` и `TCP/7777` открыты снаружи;
- [ ] `cslvpn server` запущен как служба (`systemctl is-active cslvpn`);
- [ ] `curl /api/v1/health` отвечает `{"status":"ok"}`;
- [ ] админ-API слушает только `127.0.0.1` (при необходимости задан токен);
- [ ] сеть создана (клиентом с `-create` или через `POST /api/v1/networks`);
- [ ] минимум две машины в одной сети видят друг друга по `26.x.y.z`;
- [ ] машина в другой сети **не** видит их (проверено ping'ом);
- [ ] журнал `journalctl -u cslvpn` без повторяющихся ошибок рукопожатий.

Дальше: режим шлюза (§9), предсоздание сетей и kick через админ-API (§4),
ограничения и план развития — [ROADMAP.md](ROADMAP.md).
