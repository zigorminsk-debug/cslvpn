# Windows: что работает, а что нужно донастроить

Один и тот же бинарник `cslvpn.exe` собирается под Windows amd64/arm64
(артефакт `cslvpn-windows-amd64.exe` в Actions). Протокол, шифрование,
ретранслятор и клиентская логика полностью кроссплатформенны; особенность
Windows — **создание виртуального сетевого адаптера**.

## 1. Что уже работает «из коробки»

* `cslvpn server` — ретранслятор (UDP+TCP, админ-API, статистика);
* `cslvpn client` — рукопожатие, шифрование, реконнект, выбор UDP/TCP;
* `cslvpn status` — запрос к админ-API.

Проверить без адаптера:

```powershell
.\cslvpn.exe server -listen :7777 -admin 127.0.0.1:7778
.\cslvpn.exe status -admin http://127.0.0.1:7778
.\cslvpn.exe client -server 127.0.0.1:7777 -network test -password p -create
# клиент подключится, получит адрес; создание TUN-адаптера выдаст понятную ошибку
```

## 2. Виртуальный адаптер: варианты

Чистый Go не умеет создавать NDIS-адаптеры без драйвера. Варианты, от простого
к «полному»:

### 2.1. Готовый Wintun-адаптер (рекомендуется)

1. Скачайте драйвер **Wintun** (используется WireGuard) и создайте адаптер:
   ```powershell
   # пример: адаптер "CSLVPN" создаётся установщиком WireGuard или утилитой wintun
   ```
2. Отдайте открытый хэндл адаптера клиенту через переменную окружения:
   ```powershell
   set CSLVPN_TUN_FILE=\\.\Global\CSLVPN-tun
   .\cslvpn.exe client -server 203.0.113.10:7777 -network office -password s3cret
   ```
3. Настройте адрес и маршрут (клиент печатает выданный адрес `26.x.y.z`):
   ```powershell
   netsh interface ip set address name="CSLVPN" static 26.0.0.2 255.255.255.0
   route add 26.0.0.0 mask 255.255.255.0 26.0.0.2
   ```

Клиент сам печатает готовые команды при старте (`READY: your address …`);
автоматическая настройка интерфейса на Windows в текущей сборке отключена и
возвращает предупреждение, а не ошибку.

### 2.2. Запуск клиента в WSL2

Если менять драйверы на Windows не хочется:

```bash
# внутри WSL2 (Ubuntu), сеть WSL видит Windows-хост
sudo apt install iproute2
sudo ./cslvpn client -server 203.0.113.10:7777 -network office -password s3cret
```

Это полноценный Linux-клиент (TUN + маршруты) внутри WSL; доступ к
Windows-сервисам — через `localhost`/IP хоста.

### 2.3. Своя сборка с wintun-go

Когда нужен «нативный» опыт, добавьте зависимость
`golang.zx2c4.com/wireguard/wintun` (+ `wintun.dll` рядом с exe) и замените
`internal/tun/tun_windows.go` на реализацию через неё: интерфейс
`Create/Read/Write/Close/Name` уже совпадает, остальной код менять не нужно.
Это запланировано в [ROADMAP.md](ROADMAP.md).

## 3. Права и служба

Клиенту нужны права администратора (создание адаптера и изменение таблицы
маршрутов). Для автозапуска — Планировщик задач с «Run with highest
privileges» или служба через NSSM:

```powershell
nssm install cslvpn "C:\cslvpn\cslvpn.exe" client -server 203.0.113.10:7777 -network office -password s3cret -transport tcp
nssm set cslvpn AppEnvironmentExtra CSLVPN_PASSWORD=s3cret
nssm start cslvpn
```

Сервер как служба:

```powershell
nssm install cslvpn-relay "C:\cslvpn\cslvpn.exe" server -listen :7777 -admin 127.0.0.1:7778
nssm start cslvpn-relay
New-NetFirewallRule -DisplayName "CSLVPN" -Direction Inbound -Protocol UDP -LocalPort 7777 -Action Allow
New-NetFirewallRule -DisplayName "CSLVPN TCP" -Direction Inbound -Protocol TCP -LocalPort 7777 -Action Allow
```

## 4. Брандмауэр

Windows Defender Firewall по умолчанию блокирует входящие. Для сервера
разрешите `UDP/7777` и `TCP/7777`; для клиента входящие не нужны.
Виртуальный адаптер Windows обычно классифицирует как «Неопознанная сеть»
(Public profile) — это нормально, но правила для своих приложений (SMB, RDP,
игры) нужно применять к профилю Public или переключить адаптер на Private:

```powershell
Get-NetConnectionProfile | Where-Object InterfaceAlias -eq "CSLVPN" |
  Set-NetConnectionProfile -NetworkCategory Private
```

## 5. Ограничения текущей Windows-сборки

| Возможность | Статус |
|---|---|
| Ретранслятор, админ-API, статус | работает |
| Рукопожатие, шифрование, UDP/TCP | работает |
| Создание виртуального адаптера | нужен Wintun (см. §2) или WSL2 |
| Автонастройка адреса/маршрута | только предупреждение в логе; команды печатаются |
| Интернет через сервер (`-internet`) | после настройки адаптера — `route` двумя командами |
| Kill-switch, split-tunnel GUI | в разработке (ROADMAP) |
