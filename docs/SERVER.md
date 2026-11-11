# Развёртывание сервера-ретранслятора

Сервер нужен один: VPS/выделенный хост с **белым IP** (или хотя бы с
публичным портом). Клиенты могут быть за любым NAT — им достаточно исходящего
доступа.

## 1. Требования

* Linux x86_64/arm64 (проверено на Debian/Ubuntu), Windows Server тоже
  поддерживается (см. [WINDOWS.md](WINDOWS.md));
* открытые `UDP/7777` и `TCP/7777` (или один из них, если нужен только один
  транспорт);
* ~20–50 МБ RAM на 100 сессий, CPU почти не расходуется (шифрование
  AES-NI-ускоренное);
* `iproute2` для режима шлюза, `iptables`/`nft` для NAT.

## 2. Быстрый старт

```bash
# 1. скачать бинарник (артефакт из GitHub Actions или релиз)
wget https://github.com/zigorminsk-debug/cslvpn/releases/latest/download/cslvpn-linux-amd64
chmod +x cslvpn-linux-amd64
sudo mv cslvpn-linux-amd64 /usr/local/bin/cslvpn

# 2. запустить ретранслятор (UDP и TCP на одном порту)
sudo cslvpn server -listen :7777 -admin 127.0.0.1:7778

# 3. проверить
curl -s http://127.0.0.1:7778/api/v1/stats
```

Открыть порты (вариант для ufw):

```bash
sudo ufw allow 7777/udp
sudo ufw allow 7777/tcp
```

Проверка «снаружи»: с другой машины

```bash
nc -vzu <IP> 7777   # UDP (ответа не будет, важен отсутствие ошибки)
nc -vz  <IP> 7777   # TCP -> "succeeded"
```

## 3. Параметры сервера

| Флаг | По умолчанию | Описание |
|---|---|---|
| `-listen` | `:7777` | адрес сразу для UDP и TCP |
| `-udp` / `-tcp` | из `-listen` | отдельные адреса; `off` отключает транспорт |
| `-admin` | — | админ-API, например `127.0.0.1:7778` |
| `-admin-token` | `$CSLVPN_ADMIN_TOKEN` | Bearer-токен для админ-API |
| `-max-members` | 150 | участников в одной сети (как у Radmin VPN) |
| `-max-networks` | 4096 | всего сетей на сервере |
| `-allow-create` | true | разрешить клиентам создавать сети |
| `-idle-network` | 24h | удалить сеть, пустую дольше этого времени |
| `-ban` | 15m | на сколько банить имя после kick |
| `-gateway` | false | режим интернет-шлюза (см. §5) |
| `-gateway-iface` | — | имя TUN-интерфейса шлюза |
| `-nat-cidr` | `26.0.0.0/8` | что NAT-ить в режиме шлюза |
| `-debug` | false | логировать каждый пересланный пакет (для отладки) |

Пример с ограничениями и предсозданными сетями:

```bash
sudo cslvpn server -listen :7777 -admin 127.0.0.1:7778 \
  -admin-token "$(openssl rand -hex 16)" \
  -max-members 50 -allow-create false
# теперь сеть нужно создать через API:
curl -s -X POST http://127.0.0.1:7778/api/v1/networks \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"office","password":"s3cret"}'
```

## 4. Админ-API

Все ответы — JSON. Если задан `-admin-token`, нужен заголовок
`Authorization: Bearer <token>`.

| Метод и путь | Описание |
|---|---|
| `GET /api/v1/health` | проверка живости |
| `GET /api/v1/stats` | счётчики и список сетей |
| `POST /api/v1/networks` | создать сеть `{"name":"…","password":"…"}` |
| `GET /api/v1/networks/{name}/members` | участники: имя, IP, транспорт, байты |
| `DELETE /api/v1/networks/{name}/members/{member}` | кикнуть участника (бан на `-ban`) |

Удобная обёртка из того же бинарника:

```bash
cslvpn status -admin http://127.0.0.1:7778 -token "$TOKEN"
```

```
relay http://127.0.0.1:7778
  sessions=3 networks=2 online=3 rxFrames=1841 txFrames=1830 dropped=12
  network "office"            subnet 26.0.0.0/24      members 2
  network "home"              subnet 26.0.1.0/24      members 1
```

## 5. Интернет через сервер (режим шлюза)

Опционально: клиенты могут ходить в интернет через ретранслятор (как в
обычном VPN), получая при этом изоляцию сетей.

Шаг 1 — создать TUN-интерфейс шлюза:

```bash
sudo ip tuntap add mode tun dev cslgw
sudo ip addr add 10.77.0.1/24 dev cslgw
sudo ip link set cslgw mtu 1400 up
sudo sysctl -w net.ipv4.ip_forward=1
```

Шаг 2 — запустить сервер в режиме шлюза:

```bash
sudo cslvpn server -listen :7777 -admin 127.0.0.1:7778 \
  -gateway -gateway-iface cslgw
```

Сервер сам добавит:

```
iptables -t nat -A POSTROUTING -s 26.0.0.0/8 ! -o cslgw -j MASQUERADE
iptables -A FORWARD -i cslgw -j ACCEPT
iptables -A FORWARD -o cslgw -m state --state RELATED,ESTABLISHED -j ACCEPT
ip route replace 26.0.0.0/8 dev cslgw
```

Шаг 3 — клиент:

```bash
sudo cslvpn client -server <IP>:7777 -network office -password s3cret -internet
```

Клиент сам поставит default route в туннель и закрепит /32-маршрут до
ретранслятора через прежний шлюз (иначе шифрованные пакеты ушли бы в туннель).

> Безопасность: NAT-ятся только адреса `26.0.0.0/8`, между сетями трафик
> по-прежнему не ходит. Если нужен доступ только к интернету, но не друг к
> другу — уменьшите `-nat-cidr` и не используйте `-internet` совместно с
> общими сетями.

## 6. Установка пакетом

Быстрее всего поставить ретранслятор штатным пакетом — Workflow `packages`
собирает их на каждый push, а к тегам `v*` они прикладываются к релизу:

```bash
# Debian/Ubuntu (amd64, arm64, armv7)
sudo dpkg -i cslvpn_<ver>_amd64.deb
# RHEL/Fedora (x86_64, aarch64, armv7hl)
sudo rpm -Uvh cslvpn-<ver>-1.x86_64.rpm
# Alpine
sudo apk add --allow-untrusted cslvpn_<ver>_x86_64.apk
```

Пакет кладёт бинарник в `/usr/bin/cslvpn`, юниты — в
`/usr/lib/systemd/system/`, образцы конфигов — в `/etc/cslvpn/*.example`
(0640), документацию — в `/usr/share/doc/cslvpn/`. Сервисы намеренно **не
включаются** автоматически: сначала правьте конфиг.

```bash
sudo cp /etc/cslvpn/server.env.example /etc/cslvpn/server.env
sudo systemctl enable --now cslvpn.service      # ретранслятор
sudo systemctl enable --now cslvpn-client.service  # только если это клиент
curl -s http://127.0.0.1:7778/api/v1/health     # {"status": "ok"}
```

Юнит `cslvpn.service` работает под `DynamicUser=yes` со строгим hardening
(`ProtectSystem=strict`, `PrivateTmp`, `NoNewPrivileges`, только
`CAP_NET_BIND_SERVICE`/`CAP_NET_ADMIN` при `-gateway`), `StateDirectory=cslvpn`,
`EnvironmentFile=-/etc/cslvpn/server.env`. Удаление пакета останавливает и
отключает сервисы, но `/etc/cslvpn` сохраняет — конфигурация не теряется.

Полный список того, что ставится, и грабли сборки — `packaging/README.md`.

## 7. systemd вручную

Если ставите бинарник без пакета — `/etc/systemd/system/cslvpn.service`:

```ini
[Unit]
Description=CSLVPN relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cslvpn server -listen :7777 -admin 127.0.0.1:7778
Restart=always
RestartSec=2
Environment=CSLVPN_ADMIN_TOKEN=change-me
# для режима шлюза раскомментировать:
# ExecStart=/usr/local/bin/cslvpn server -listen :7777 -gateway -gateway-iface cslgw
# AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW
# User=root

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now cslvpn
journalctl -u cslvpn -f
```

## 8. Docker

`Dockerfile` в корне репозитория собирает статический бинарник. Режим
ретранслятора требует-NAT только для `-gateway`:

```bash
docker build -t cslvpn .
docker run -d --name cslvpn --network host cslvpn \
  server -listen :7777 -admin 127.0.0.1:7778
```

`--network host` нужен, чтобы UDP и TCP были видны снаружи и чтобы работал
режим шлюза (TUN + iptables на хосте).

## 9. Мониторинг и эксплуатация

* `GET /api/v1/stats` → `counters.sessions`, `rx_frames`, `tx_frames`,
  `rx_bytes`, `tx_bytes`, `dropped`, `handshakes`;
* рост `dropped` на здоровом сервере — это, как правило, повторы UDP и
  служебные кадры; устойчивый рост вместе с жалобами на связь — повод включить
  `-debug` и посмотреть, доходят ли пакеты до шлюза;
* пустые сети удаляются автоматически через `-idle-network`;
* логи содержат только метаданные (кто, когда, откуда, сколько), содержимое
  туннеля не пишется никогда.

## 10. Производительность

* один vCPU тянет несколько сотен Мбит/с ретрансляции (AES-NI);
* UDP-сокет использует буферы 4 МБ; при большом числе участников поднимите
  `net.core.rmem_max`/`wmem_max`;
* MTU туннеля 1400 — безопасно для любого пути (инкапсуляция 44 байта при
  IPv4+UDP, запас под PPPoE);
* `-max-members` ограничивает и память, и широковещание списка пиров.
