# jbod-go

Перенос [Gandi/jbod-rs](https://github.com/Gandi/jbod-rs) на Go.
Исходная ревизия: `54fb20260aa0d5c88855fb71f3b9b7faf2d21e13`.
Лицензия BSD-2-Clause; исходные уведомления сохранены в LICENSE.

English version: [README.en.md](README.en.md).

## Требования и сборка

Go 1.25+, одна зависимость — [spf13/pflag](https://github.com/spf13/pflag)
для POSIX-разбора флагов. Работа с оборудованием требует Linux,
драйвера enclosure, доступного /sys/class/enclosure и утилит
lsscsi, sg_inq, sg_map, sg_ses, sginfo, scsi_temperature.
На Debian/Ubuntu установите пакеты lsscsi и sg3-utils.
Доступ к устройствам /dev/sg* и запись LED требуют соответствующих прав
(обычно root). Справка и тесты работают без оборудования, в том числе на macOS.

Перед работой оба бинарника выполняют preflight: проверяют наличие всех
утилит и читаемость `/sys/class/enclosure` и печатают единым списком всё,
чего не хватает, с именами пакетов. Утилиты резолвятся один раз в абсолютные
пути по фиксированному `PATH=/usr/sbin:/usr/bin:/sbin:/bin`, а запускаются с
`LC_ALL=C` — парсеры завязаны на английский вывод sg3-utils, и демон под root
не должен зависеть от унаследованного окружения.

Модуль называется `github.com/kmlebedev/jbod-go`, поэтому бинарники можно
поставить и без клонирования:

```sh
go install github.com/kmlebedev/jbod-go/cmd/jbod@latest
go install github.com/kmlebedev/jbod-go/cmd/prometheus-jbod-exporter@latest
```

```sh
make build
./bin/jbod help
./bin/jbod list -e
./bin/jbod list -d
./bin/jbod list -ed
./bin/jbod list -f
./bin/jbod list -ef
./bin/jbod list --slots
./bin/jbod list --slots --enclosure-id naa.50050cc10c400000 --json
./bin/jbod capabilities
./bin/jbod capabilities --enclosure naa.50050cc10c400000 --json
sudo ./bin/jbod led --locate /dev/sda --on
sudo ./bin/jbod led --locate /dev/sda --off
sudo ./bin/jbod led --fault /dev/sg1 --on
sudo ./bin/jbod led --enclosure naa.50050cc10c400000 --locate 5 --on
./bin/jbod prometheus --ip-address 127.0.0.1 --port 9945
```

Флаги `-e`, `-d`, `-f` и `-s` независимы: каждый добавляет свою секцию вывода,
поэтому `list -ef` печатает и корпуса, и вентиляторы. Диски упорядочены
натурально — `Slot 2` идёт перед `Slot 10`.

Разбор флагов POSIX-совместимый: короткие группируются (`-ed`, `-edf`),
длинные принимают `--flag=value`, `--` завершает опции, `--help` есть у каждой
подкоманды. Для экспортёра `--ip` и `--ip-address` — одно и то же имя, а не два
флага, где молча выигрывает последний.

LED можно указывать несколько раз: `led -l /dev/sda -l /dev/sdb --on`.
Поддерживаются пути устройств /dev/sg* и соответствующие /dev/sd*.
Операции выполняются по очереди; ошибка останавливает выполнение,
предыдущие успешные записи не откатываются.

## Слоты, адресация и capabilities

Слот и диск — разные сущности. Обход начинается не с `device/scsi_generic`,
а с компонентов корпуса, поэтому пустая корзина существует в модели и её
видно в выводе:

```
$ jbod list --slots
Enclosure 1:0:0:0  id naa.50050cc10c400000 (logical)
SLOT  NAME            TYPE          STATUS         OCCUPANCY    DEVICE    MAP       LOCATE  FAULT             POWER
1     Slot 01, front  array device  OK             occupied     /dev/sg1  /dev/sda  off     off               on
2     Slot 02, front  array device  not installed  empty        -         -         on      requested         on
3     Slot 03, front  array device  unavailable    unavailable  -         -         -       sensed+requested  -
```

Три состояния занятости различаются намеренно:

| Occupancy | Значение |
| --- | --- |
| `occupied` | к слоту подключено устройство |
| `empty` | устройства нет, и корпус ответил: либо `not installed`, либо другой статус |
| `unavailable` | слот не удалось прочитать, либо корпус сообщает `unavailable`/`unsupported` |

Нечитаемый слот никогда не выдаётся за пустой: колонка `-` означает
«корпус не отдаёт такой атрибут», а не «ноль». В JSON это `null`.

Колонка FAULT разделена на две половины, как их кодирует SES: драйвер
кладёт в sysfs-атрибут `(status[3] & 0x60) >> 5`, где бит 6 — FAULT SENSED
(корпус сам обнаружил неисправность), а бит 5 — RQST FAULT (кто-то зажёг
индикатор). Поэтому `sensed` — это авария, `requested` — метка оператора, и
смешивать их нельзя. Запись выставляет только `requested`, и readback
сравнивает именно его.

Адресация корпуса. Приоритет: логический идентификатор (`/sys/class/enclosure/*/id`,
его заполняет SES-бэкенд), затем unit serial number из `sg_inq`, и только
потом SCSI-адрес. Первые два переживают перезагрузку, третий — нет, и там,
где используется он, вывод помечает идентификатор как `temporary`.
`--enclosure-id` принимает любое из трёх написаний; в `capabilities` и `led`
то же самое можно писать короче — `--enclosure`. В `list` короткое имя
занято: там `--enclosure` — это секция вывода, как и было.

Слот адресуется номером, который отдаёт корпус, или именем компонента:

```sh
sudo jbod led --locate 1:0:0:0/5 --on                          # самодостаточно
sudo jbod led --enclosure naa.50050cc10c400000 --locate 5 --on # то же самое
sudo jbod led --locate "1:0:0:0/Slot 05, front" --on           # по имени компонента
sudo jbod led --locate /dev/sda --on                           # как раньше
```

Пустую корзину можно зажечь только так: у неё нет пути устройства.

После записи состояние проверяется чтением с ограниченным ожиданием
(`--readback-timeout`, по умолчанию 1s). Успешный системный вызов не
выдаётся за подтверждённое изменение:

```
/dev/sda locate: on (confirmed)
1:0:0:0/5 fault: on (NOT confirmed: reads back as off)
1:0:0:0/5 locate: off (write accepted, no readback available)
```

Первая строка — подтверждено чтением. Вторая — корпус ответил и ответил
другим состоянием: это ошибка и ненулевой код выхода. Третья — атрибут не
читается обратно вообще; это не ошибка, но и не подтверждение. Если слот
исчез во время операции (диск вынули), команда сообщает именно это, а не
отказ в правах.

`jbod capabilities` показывает, что корпус умеет, отдельно на чтение и на
запись, и на чём основан вывод:

```
$ jbod capabilities
Enclosure 1:0:0:0  address naa.50050cc10c400000 (stable)  components 24
CAPABILITY         READ         WRITE        EVIDENCE
slot.enumeration   supported    unsupported  sysfs: 24 component directories
led.locate         supported    unknown      sysfs: 24/24 components expose locate, 24 readable; ...
slot.power_status  unsupported  unsupported  no component exposes power_status
disk.temperature   unknown      unsupported  scsi_temperature: installed; ...
```

Правила вывода:

- обнаружение ничего не пишет и ничего не переключает;
- `unsupported` на запись означает, что атрибут read-only, то есть у драйвера
  нет обработчика записи (sysfs создаёт такой атрибут с режимом 0444);
- `unknown` на запись — атрибут писать можно, но корпус вправе принять
  control page и проигнорировать её, а ядро всё равно вернёт успех. Поэтому
  обнаружение никогда не объявляет запись `supported`; это делает только
  readback после настоящей записи;
- ошибка транспорта или доступа показывается отдельным полем `[error: ...]`
  и не превращается в `unsupported`.

`--json` есть у `list`, `capabilities` и `led`. Отсутствующее значение — это
`null`, а не ноль; секции, которые не запрашивали, в документе отсутствуют,
а запрошенная и пустая — это `[]`.

## Prometheus

Оба способа запуска используют один экспортёр:

```sh
./bin/jbod prometheus -i 127.0.0.1 -p 9945
./bin/prometheus-jbod-exporter 127.0.0.1 9945
```

Оба бинарника понимают `--help` и `--version`. Версия берётся из
`-ldflags -X` (Makefile подставляет `git describe --tags --always --dirty`),
а если сборка без стампа — из `runtime/debug.ReadBuildInfo`, то есть
`go install ...@v1.2.3` тоже отчитывается честно.

По умолчанию слушает 127.0.0.1:9945 — процесс имеет доступ к `/dev/sg*`
и обычно работает от root, поэтому выход в сеть должен быть осознанным.
При биндинге на wildcard (`0.0.0.0`, `::`) в лог выводится предупреждение.
GET / возвращает пустой ответ; GET /metrics — Prometheus text format 0.0.4.

Флаги настройки (`--help` показывает их значения по умолчанию):

| Флаг | По умолчанию | Назначение |
| --- | --- | --- |
| `--command-timeout` | 15s | таймаут одной внешней команды |
| `--scrape-timeout` | 2m | таймаут полного сбора |
| `--concurrency` | 12 | сколько внешних команд выполняется одновременно |
| `--cache-ttl` | 0s | отдавать предыдущий снимок в течение этого времени |
| `--deprecated-metrics` | true | отдавать также серии до 1.1 (`jbod_fan_rpm`) |
| `--log-level` | info | debug, info, warn или error |
| `--log-format` | json | json для systemd, text для терминала |

Внешние команды выполняются параллельно с ограничением `--concurrency`:
на полку в 60 дисков приходится 120 запусков процессов, последовательно они
не укладываются в интервал scrape. Порядок вывода от параллелизма не зависит.

Одновременные scrape объединяются в один проход по оборудованию, поэтому
`curl /metrics` рядом с Prometheus не удваивает нагрузку на экспандер.
`--cache-ttl` дополнительно отдаёт недавний результат вообще без обращения
к оборудованию; разумное значение — около половины `scrape_interval`.

SIGINT/SIGTERM отменяют выполняющийся scrape сразу, не дожидаясь таймаута
команды.

## Логирование

Экспортёр пишет структурированный лог в stderr (`log/slog`): по умолчанию JSON,
чтобы journald индексировал поля. Логируются старт с фактическими параметрами,
длительность каждого сбора, каждая упавшая команда (с указанием collector) и
остановка. Успешный сбор — на уровне debug, сбор с потерями — info, поэтому при
`--log-level info` в журнале видно только проблемное.

```sh
journalctl -u prometheus-jbod-exporter -o cat | jq 'select(.msg=="collection error")'
```

CLI (`list`, `led`) по умолчанию молчит: об отсутствующих значениях говорит сама
таблица (`ERR`, `N/A`, `NONE`), а ошибки возвращаются кодом выхода и строкой в
stderr.

Сохранены имена и наборы labels:

| Метрика | Labels |
| --- | --- |
| number_of_enclosures | нет |
| jbod_slot_temperature | slot, enclosure |
| jbod_fan_rpm | device, slot — **deprecated**, см. ниже |

Добавлены в 1.1:

| Метрика | Тип | Labels | Значение |
| --- | --- | --- | --- |
| jbod_enclosure_info | gauge | enclosure, enclosure_id, id_source, vendor, model, revision, serial | идентичность корпуса, всегда 1 |
| jbod_enclosure_slots | gauge | enclosure, enclosure_id, occupancy | число слотов в каждом состоянии |
| jbod_fan_speed_rpm | gauge | enclosure, enclosure_id, component, component_id | RPM вентилятора без коллизий |

Добавлены метрики состояния самого сбора:

| Метрика | Тип | Значение |
| --- | --- | --- |
| jbod_up | gauge | 1, если сбор завершился полностью |
| jbod_scrape_duration_seconds | gauge | длительность последнего сбора |
| jbod_scrape_errors_total | counter | накопленные ошибки по collector (enclosures, slots, disks, fans, led) |

`jbod_enclosure_info` — точка join для всех серий, у которых в labels стоит
SCSI-адрес: адрес назначается при сканировании и меняется, поэтому дашборд,
которому нужна устойчивая идентичность, джойнит по `enclosure` и берёт
`enclosure_id`. Label `id_source` говорит, настоящий это идентификатор
(`logical`, `serial`) или снова адрес (`address`).

Метрики самого процесса (`process_cpu_seconds_total`,
`process_resident_memory_bytes`, `process_virtual_memory_bytes`,
`process_start_time_seconds`, `process_open_fds`, `process_max_fds`) читаются
из `/proc/self` при каждом запросе и не кешируются: в Rust-версии их отдавал
крейт prometheus, и дашборды по ним ломались бы без них. На системах без
`/proc` (например macOS) они просто не выводятся — лучше отсутствие серии,
чем нули.

### Миграция с jbod_fan_rpm

В `jbod_fan_rpm` label `device` — это описание вентилятора, а `slot` — индекс
sg_ses. Оба значения локальны для корпуса, поэтому «Fan A» с индексом `2,0`
совпадает по labels у каждой полки в стойке, и значение последней затирает
предыдущие. Исправить это на месте нельзя: смена набора labels изменила бы
смысл серии, которую уже читают дашборды.

Поэтому исправленная метрика — это новое имя:

```
jbod_fan_speed_rpm{enclosure="1:0:0:0",enclosure_id="naa.5000...01",component="Fan A",component_id="2,0"} 1200
jbod_fan_speed_rpm{enclosure="10:0:0:0",enclosure_id="naa.5000...02",component="Fan A",component_id="2,0"} 4800
```

Порядок миграции:

1. обновить экспортёр: обе серии отдаются одновременно, ничего не ломается;
2. перевести дашборды и правила на `jbod_fan_speed_rpm`;
3. запустить экспортёр с `--deprecated-metrics=false` и убедиться, что ничего
   не пропало;
4. оставить так. Удаление `jbod_fan_rpm` запланировано не раньше 2.0.

Каждый scrape собирает свежие значения. Исчезнувшие устройства удаляются
из выдачи. Недоступная температура пропускается (в CLI отображается ERR);
недоступная прошивка отображается как N/A.

Частичный сбор — это HTTP 200: сломанный датчик, недоступное дерево sysfs
одного корпуса или вентилятор без RPM учитываются в `jbod_scrape_errors_total`,
а всё остальное отдаётся как обычно. HTTP 503 остаётся только для полного
отказа — когда не удалось получить сам список корпусов (нет утилиты lsscsi,
нет драйвера). CLI по-прежнему считает такие ошибки фатальными и не печатает
неполную таблицу; исключение — вентилятор без RPM, он пропускается.

## Отличия от Rust-версии

- Экспортёр работает на переднем плане; SIGINT/SIGTERM корректно останавливают HTTP.
  Для фоновой работы используется systemd, без fork и второго дочернего процесса.
- Таблицы текстовые, без цветовых и мигающих ANSI-последовательностей.
- Утилиты резолвятся по фиксированному PATH в абсолютные пути и запускаются
  с чистым окружением; справка не требует установленных SCSI-утилит.
- Разбор флагов на pflag, поэтому совместимость с clap буквальная, а не «на глаз».
- Ошибки возвращаются с ненулевым кодом вместо panic или молчаливого успеха.
- VPD page 0x80 разбирается с учётом бинарного заголовка и длины.
- Слот и диск разделены: перечисляются все корзины корпуса, включая пустые,
  вместе с номером, типом, статусом, питанием и состоянием индикаторов.
  Нестандартные имена компонентов поддерживаются.
- LED не зависит от доступности scsi_temperature и sginfo; результат записи
  проверяется чтением, и неподтверждённая запись так и называется.
- Есть `capabilities` и `--json` у `list`, `capabilities` и `led`.
- Требуется ровно одно состояние --on/--off. Неизвестные устройства — ошибка.
- Совместимость метрик полная, включая `process_*`; сверх Rust-версии есть
  `jbod_up`, `jbod_scrape_duration_seconds`, `jbod_scrape_errors_total`,
  `jbod_enclosure_info`, `jbod_enclosure_slots` и `jbod_fan_speed_rpm`.

## Установка и Debian

```sh
sudo make install
sudo systemctl daemon-reload
sudo systemctl enable --now prometheus-jbod-exporter
```

`make install` ставит оба бинарника в /usr/bin, unit в /lib/systemd/system
и файл аргументов в /etc/default/prometheus-jbod-exporter. Для установки в
staging-каталог задайте DESTDIR. При изменении PREFIX скорректируйте ExecStart
в unit-файле.

Аргументы задаются не правкой unit-файла, а `/etc/default/prometheus-jbod-exporter`
(unit читает его через `EnvironmentFile=-` и подставляет `$ARGS`). Экспортёр
по умолчанию слушает только 127.0.0.1, поэтому для scrape с другого хоста:

```sh
echo 'ARGS="--ip-address 10.0.0.7 --port 9945"' > /etc/default/prometheus-jbod-exporter
systemctl restart prometheus-jbod-exporter
```

Unit сознательно не включает `PrivateDevices=` — он спрятал бы `/dev/sg*`,
ради которых экспортёр и существует. Остальное усиление на месте:
`ProtectSystem=strict`, `NoNewPrivileges`, `ProtectHome`, `PrivateTmp`,
`ProtectKernel*`, `RestrictNamespaces`, `MemoryDenyWriteExecute`,
`SystemCallFilter=@system-service`,
`CapabilityBoundingSet=CAP_SYS_RAWIO CAP_DAC_OVERRIDE`.

Если preflight не проходит (нет утилит, не читается `/sys/class/enclosure`),
экспортёр не стартует и пишет причину в журнал — с `Restart=on-failure` это
даёт цикл перезапусков до исправления, зато причина видна сразу.

Сборке нужен доступ к модулям (`go mod download`) или каталог `vendor/`
(`make vendor`) — единственная зависимость pflag в репозитории не лежит.

На целевой Linux-системе с dpkg можно собрать пакет: `make deb`.
Результат: `dist/jbod-go_<версия>_<арх>.deb` и `dist/SHA256SUMS`. Версия
пакета берётся из `git describe`; если тегов ещё нет, `describe` отдаёт голый
хеш, и версия становится `0.0.0+<хеш>` — Debian требует, чтобы версия
начиналась с цифры. Maintainer берётся из `git config user.name/email`, а если
идентичности нет (типичный CI-раннер), нужно передать свою:
`make deb MAINTAINER="Имя <адрес>"`. Пакет содержит conffiles, md5sums и
postinst/prerm/postrm с `deb-systemd-helper`. Сборка Debian-пакета на macOS
не проверялась (нет dpkg-deb).

Релизы собирает goreleaser по тегу `v*` (`.goreleaser.yaml`,
`.github/workflows/release.yml`): архивы для linux/amd64 и linux/arm64,
`.deb` через nfpm и `SHA256SUMS`.

## Проверка

```sh
make test      # go vet + go test -race
make lint      # гейт gofmt + golangci-lint, если установлен
make cover     # покрытие с -covermode=atomic
GOOS=linux GOARCH=amd64 go build ./...
GOOS=linux GOARCH=arm64 go build ./...
```

Тесты используют временное дерево sysfs и подставные ответы SCSI-утилит:
обнаружение оборудования, VPD, температура, RPM, управление LED,
совместимые метрики, исчезновение устройств, HTTP-ошибки и CLI.
Отдельно проверяются параллельный сбор и соблюдение лимита конкурентности,
объединение одновременных scrape в один проход, кеш по TTL, частичный сбор
с метриками состояния и отмена выполняющегося scrape при остановке демона.
Парсеры вывода утилит покрыты табличными тестами и фаззингом:

```sh
go test -run xxx -fuzz FuzzParseVPD80 -fuzztime 30s ./internal/jbod/
```

Вывод `list` и текст метрик зафиксированы golden-файлами, поэтому лишняя или
исчезнувшая строка видна как diff, а не проходит незамеченной:

```sh
go test ./internal/cli/ ./internal/metrics/ -update   # переписать golden
```

CI (`.github/workflows/go.yml`) прогоняет тесты на Go 1.25 и 1.26, гейт
`gofmt`, `go vet`, golangci-lint, govulncheck, короткий фаззинг парсеров,
сборку под linux/amd64 и linux/arm64 и сборку `.deb` с `dpkg-deb --contents`.

На реальном JBOD проверки не выполнялись. Перед эксплуатацией проверьте
вывод list и метрики на своём оборудовании; тестовые ответы не заменяют
проверку разных версий sg3-utils и моделей корпусов.

Структура:
- cmd/jbod, cmd/prometheus-jbod-exporter — entry points, по одной строке каждый:
  общее тело обоих бинарников в `internal/cli.Main`.
- internal/jbod — доменные типы, сбор через sysfs и sg3-utils, LED;
  `parse.go` — чистые парсеры вывода утилит (табличные тесты и фаззинг),
  `order.go` — натуральная сортировка слотов.
- internal/metrics — кодирование снимка в Prometheus text format 0.0.4.
- internal/exporter — HTTP-обработчик, таймаут scrape, объединение
  одновременных scrape и кеш по TTL.
- internal/process — метрики `process_*` из `/proc/self`.
- internal/cli — разбор аргументов: `list.go`, `led.go`, `prometheus.go`,
  вывод таблиц в `output.go`, версия в `version.go`, логгер в `log.go`.
