# Установочные пакеты

Каталог описывает, как из одного бинарника `cslvpn` получаются штатные
пакеты для всех поддерживаемых ОС. Всё собирается автоматически в GitHub
Actions (workflow `.github/workflows/packages.yml`) на каждый push; вручную
запускать ничего не нужно.

| Что | Чем собирается | Файлы |
|---|---|---|
| `.deb` / `.rpm` / `.apk` | [nfpm](https://nfpm.goreleaser.com) | `nfpm.yaml`, `systemd/`, `scripts/`, `config/` |
| `.tar.gz` (portable) | `tar` | — |
| `.msi` | WiX v5 (`dotnet tool`) | `wix/cslvpn.wxs` |
| `setup.exe` | NSIS (choco) | `nsis/cslvpn.nsi` |
| `.pkg` | `pkgbuild` (штатный в macOS) | `macos/build-pkg.sh` |

## Что попадает в систему

Linux (deb/rpm/apk):

```
/usr/bin/cslvpn
/usr/lib/systemd/system/cslvpn.service          # ретранслятор
/usr/lib/systemd/system/cslvpn-client.service   # клиент
/etc/cslvpn/server.env.example                  # 0640 root:root
/etc/cslvpn/client.env.example                  # 0640 root:root
/usr/share/doc/cslvpn/{README.md,LICENSE,docs/,examples/}
```

`postinstall` делает `systemctl daemon-reload` и печатает подсказку, но
**не включает** сервисы: сначала нужно править `/etc/cslvpn/*.env`.
`preremove` останавливает и отключает юниты. Каталог `/etc/cslvpn`
намеренно не удаляется — конфигурация переживает удаление пакета.

Windows: `C:\Program Files\CSLVPN\cslvpn.exe` + документация,
`%ProgramData%\CSLVPN\*.env.example`, пункты Start menu, запись в
«Программы и компоненты», правила Windows Firewall для 7777/udp и 7777/tcp,
опционально служба `cslvpn-relay` (автозапуск, перезапуск при падении).
MSI кладёт `cslvpn.exe` в `PATH`.

macOS: `/usr/local/bin/cslvpn`, `/Library/LaunchDaemons/io.cslvpn.relay.plist`
(установлен, но **выключен** — `launchctl bootstrap` делает postinstall
только после явного `enable`).

## Версия пакета

* тег `v*` → версия из тега (`v0.2.0` → `0.2.0`);
* обычный push → `0.0.<GITHUB_RUN_NUMBER>`.

Схема обязана быть валидной одновременно для semver, deb, rpm, apk и MSI
(MSI требует ровно три числовых компонента < 65536), а NSIS дополнительно
проверяет `VIProductVersion` в формате `X.X.X.X` — поэтому суффиксы вида
`-<sha>` в CI не используются.

## Локальная сборка

```bash
# Linux: нужен nfpm (одним файлом с releases, либо go install)
VERSION=0.1.0 ARCH=amd64 nfpm package -f packaging/nfpm.yaml -p deb -t pkg/
VERSION=0.1.0 ARCH=arm64 nfpm package -f packaging/nfpm.yaml -p rpm -t pkg/
VERSION=0.1.0 ARCH=arm7  nfpm package -f packaging/nfpm.yaml -p apk -t pkg/

# Windows (PowerShell, из корня репозитория)
wix build -arch x64 -d Version=0.1.0 -d Src=build\cslvpn.exe `
  -d License=LICENSE -d Readme=docs\SERVER.md `
  -o pkg\cslvpn-0.1.0-windows-amd64.msi packaging\wix\cslvpn.wxs
& "C:\Program Files (x86)\NSIS\makensis.exe" /DVERSION=0.1.0 /DARCH=amd64 `
  /DSRC=$PWD\build\cslvpn.exe /DOUT=$PWD\pkg\cslvpn-0.1.0-windows-amd64-setup.exe `
  /DPROJECT=$PWD packaging\nsis\cslvpn.nsi

# macOS
./packaging/macos/build-pkg.sh "$PWD/build/cslvpn" 0.1.0 arm64 pkg/cslvpn-0.1.0-macos-arm64.pkg
```

`ARCH` для nfpm указывается в Go-стиле (`amd64`, `arm64`, `arm7`) — nfpm сам
переводит его в `x86_64` / `aarch64` / `armv7hl` для rpm и apk.

## Грабли, на которые уже наступили

Записано, чтобы не повторять:

* **nfpm v2** не знает полей `keywords` и `overrides.*.arch` — ошибка
  `yaml: unmarshal errors` и падение до сборки. Архитектуру задаёт только
  ключ `arch:` верхнего уровня; `version_schema: none`, потому что версия
  из CI (`0.0.123`) не semver-совместима по строгим правилам nfpm.
* **NSIS** разрешает относительные пути `File`/`OutFile`/`!insertmacro
  MUI_PAGE_LICENSE` от каталога скрипта, а не от CWD. Отсюда дефайн
  `${PROJECT}` — CI передаёт абсолютный путь к корню репозитория.
* `SetShellVarContext` допустим только внутри секции или функции → вызов
  перенесён в `.onInit` / `un.onInit`.
* `$COMMONAPPDATA` в NSIS не существует (это `$APPDATA` в контексте
  all-users).
* `VIProductVersion` требует четыре числа (`1.2.3.4`) — версия `0.0.123`
  дополняется `.0`.
* В WiX `.wxs` **нельзя** комментарии `;` — только XML `<!-- -->`
  (`error WIX0104: Not a valid source file`).
* Если включён `/WX`, любое предупреждение makensis — ошибка. Это полезно
  (ловит неизвестные переменные), но тогда секции должны иметь ровно те id,
  которые упомянуты в `MUI_DESCRIPTION_TEXT`.
* macOS-раннер `macos-14` больше не поддерживается hosted-пулом
  (`configuration 'macos-14-arm64-us-default' is not supported`) — берём
  `macos-latest`.
