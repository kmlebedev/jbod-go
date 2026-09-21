# jbod-go: что улучшить после порта с Rust

Ревью коммита в `/Users/whitefox/GolandProjects/jbod-go` (~600 строк Go) в сравнении
с [Gandi/jbod-rs](https://github.com/Gandi/jbod-rs) (~1500 строк Rust).

Базовое состояние хорошее: `gofmt -l` чист, `go vet ./...` молчит,
`go test -race` проходит, покрытие 84% (`internal/jbod`) и 62% (`internal/cli`),
зависимостей нет. Порт местами строго лучше оригинала (нет `.unwrap()`/`expect()`,
корректный разбор VPD 0x80, свежий снимок метрик вместо «залипающих» серий,
graceful shutdown). Ниже — то, что стоит доделать.

---

## Состояние

Все блоки выполнены; ветки `refactor_stage1`…`refactor_stage6`.

| Блок | Пункты | Где |
| --- | --- | --- |
| A | A1–A9 | stage1 (A1–A3, A8), stage2 (A6), stage6 (A4, A5, A7, A9) |
| B | B1–B6 | stage2 |
| C | C1–C7 | stage3 (C5 частично: `Run` берёт конкретный клиент, потому что экспортёру нужен производный) |
| D | D1–D4 | stage4 |
| E | pflag | stage5 — выбран pflag, все три костыля убраны |
| F | версия Go, идиомы, slog, ldflags, doc | stage5 |
| G | субтесты, golden, фаззинг, конкурентность, cmd/* | stage6 |
| H | Makefile, CI, release, systemd, deb | stage6 |
| I | README.en.md, CHANGELOG, CONTRIBUTING | stage6 |

Не проверено на живом оборудовании: ни один пункт не тестировался на реальной
полке, а сборка `.deb` — на Linux с dpkg.

---

## A. Баги

### A1. `list -e -f` молча теряет вентиляторы
`internal/cli/cli.go:79`

```go
if disks || enc {
    ...
} else {
    fs, err := c.Fans(ctx, enclosures)
}
```

`--fan` учитывается только когда не заданы `--enclosure`/`--disks`. Проверено:

```
args=[list -e -f]  out="SLOT DEVICE VENDOR MODEL REVISION SERIAL\n1:0:0:0 /dev/sg0 ..."   # вентиляторов нет
args=[list -f]     out="SLOT IDENT DESCRIPTION STATUS RPM\n1:0:0:0 2,0 Fan A low 1200"
```

При этом README рекламирует комбинированные флаги (`list -ed`), так что `-ef`
пользователь напишет обязательно. Нужно обрабатывать три флага независимо:
секция enclosures → секция disks → секция fans.

### A2. Лексикографическая сортировка слотов
`internal/jbod/jbod.go:201`

`sort.Slice` по строке даёт `Slot 1, Slot 10, Slot 11, Slot 2`. На 24/60-слотовых
полках это заметно. Нужна natural-сортировка: вынести числовой суффикс слота в
отдельное поле при разборе (`Slot int`, `SlotLabel string`) и сортировать
`slices.SortFunc` по `(Enclosure, SlotNum, SlotLabel)`.

### A3. Лишнее условие в `SetLED`
`internal/jbod/jbod.go:267`

```go
if d.Device != device && (d.Map == "NONE" || d.Map != device) {
```

`device` валидируется как `/dev/...`, поэтому никогда не равно `"NONE"`, и левая
часть дизъюнкции избыточна. Эквивалент: `if d.Device != device && d.Map != device`.
Это симптом более общей проблемы — sentinel-строк (см. C1).

### A4. Нет preflight-проверки утилит
В Rust есть `Util::verify_binary_needed()` (`src/utils/helper.rs:89`), которая
печатает единым списком все недостающие пакеты, и `verify_sysclass_folder()`,
которая при пустой `/sys/class/enclosure` подсказывает альтернативы. В Go этого
нет: пользователь получает `exec: "sg_inq": executable file not found in $PATH`
посреди сбора, а экспортёр — 503 без диагностики.

Добавить `func (c *Client) Preflight() error`, вызывать в обоих `main()`
и один раз на старте экспортёра.

### A5. Поиск бинарников через PATH под root
Rust использует абсолютные пути (`/usr/bin/lsscsi`, `/usr/bin/sg_inq`, ...).
Go резолвит через PATH, а процесс обычно root и запускается из systemd.
Как минимум: резолвить один раз через `exec.LookPath` при старте и хранить
абсолютные пути в `Client`; заодно чистить окружение (`cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}`) —
`LC_ALL=C` важен ещё и потому, что парсеры завязаны на английский вывод sg3-utils.

### A6. Одна сломанная вертушка роняет весь scrape
`internal/jbod/jbod.go:240`

```go
return nil, fmt.Errorf("no fan RPM for %s index %s", ...)
```

Ошибка на одном индексе `sg_ses` → `Metrics` возвращает ошибку → HTTP 503 → теряются
и температуры, и счётчик корпусов. То же для `Disks` при `os.ReadDir` одного корпуса
(`jbod.go:151`). Нужна модель частичного успеха (см. B5).

### A7. Выбор устройства корпуса из `lsscsi -g`
`internal/jbod/jbod.go:70` берёт **первый** токен, начинающийся с `/dev/`. У корпуса
блочного устройства нет (колонка `-`), так что обычно это и есть `/dev/sgN`, но
предположение хрупкое. Надёжнее: брать последнее поле строки и проверять
префикс `/dev/sg`.

### A8. `prometheus-jbod-exporter --version` падает
`cli.Run` умеет `--version`/`-V`, а `cli.Exporter` — нет: неизвестный флаг → ошибка
парсинга. Добавить `--version`/`--help` в экспортёр.

### A9. Потеряны `process_*` метрики
Rust собирает `prometheus::gather()` поверх своего реестра, а крейт подключён с
фичей `process`, то есть в выдаче есть `process_cpu_seconds_total`,
`process_resident_memory_bytes`, `process_open_fds` и т.д. Go-порт их не отдаёт —
существующие дашборды/алерты по ним сломаются. Либо перейти на
`prometheus/client_golang`, либо добавить минимальный сборщик из `/proc/self/{stat,statm,fd}`.

**Сделано.** Сначала был свой сборщик (`internal/process`), затем экспортёр
целиком переведён на `prometheus/client_golang`: снимок отдаётся через
`prometheus.Collector` с const-метриками, ответ пишет `promhttp`, а
`process_*` приходят из штатного сборщика библиотеки — ровно оттуда же,
откуда их брал Rust. Свой кодировщик текстового формата и свой читатель
`/proc` удалены.

---

## B. Производительность и эксплуатационная пригодность

### B1. Последовательный запуск внешних команд — главная проблема
На каждый диск в `Disks(details=true)` выполняется **два** процесса
(`scsi_temperature`, `sginfo`), на каждую вертушку — **один** `sg_ses --index=`.
Всё строго последовательно, таймаут каждой команды 15 с (`jbod.go:30`),
таймаут scrape — 2 мин (`metrics.go:81`).

Полка на 60 дисков: 120 запусков процессов. Даже при 0,5 с на команду это 60 с;
на реальных SAS-экспандерах `sginfo` нередко идёт секунду и больше — scrape не
укладывается в 2 минуты и отдаёт 503.

Исправление: пул воркеров с ограничением (8–16), `golang.org/x/sync/errgroup`
с `SetLimit` или самодельный семафор на каналах, чтобы не тянуть зависимость.
Порядок восстанавливается сортировкой (A2). Это даёт ускорение почти на порядок
и снимает необходимость в 2-минутном таймауте.

### B2. Нет дедупликации одновременных scrape
Prometheus + ручной `curl /metrics` = два полных опроса железа параллельно,
и каждый плодит сотню процессов. Нужен `singleflight` (или мьютекс + кеш
результата с TTL ~ половине scrape_interval). Для экспортёра, дёргающего железо,
это обязательный паттерн.

### B3. `http.Server` без `BaseContext`
`internal/cli/cli.go:192`. При SIGTERM `Shutdown` ждёт до 5 с, а незавершённый
scrape держится за `r.Context()`, который отменится только при `Close()`.
Достаточно `BaseContext: func(net.Listener) context.Context { return ctx }` —
тогда отмена доходит до `exec.CommandContext` сразу.

### B4. Таймауты и адрес зашиты в код
15 с в `run()`, 2 мин в `Handler()`, 5 с shutdown, 130 с WriteTimeout. Всё это
должно быть полями `Client`/`Config` и флагами (`--command-timeout`,
`--scrape-timeout`). Сейчас «для больших корпусов настройте scrape_timeout» из
README нечем поддержать со стороны экспортёра.

### B5. All-or-nothing вместо метрик здоровья
Стандартный паттерн exporter'а: всегда 200, а о проблемах сообщать метриками.
Добавить:

```
jbod_up 1
jbod_scrape_duration_seconds 12.4
jbod_scrape_errors_total{collector="fans"} 3
```

и отдавать всё, что удалось собрать. 503 оставить только на полный отказ
(нет `/sys/class/enclosure`, нет бинарников).

### B6. Слушать по умолчанию 0.0.0.0 под root
`cli.go:163`. Для процесса с правами на `/dev/sg*` дефолт стоит поменять на
`127.0.0.1` (и документировать), либо хотя бы предупреждать в логе при биндинге
на wildcard.

---

## C. Типы и API

### C1. Sentinel-строки вместо отсутствия значения
`Disk.Temperature = "ERR"`, `Firmware = "N/A"`, `Map = "NONE"`,
`Enclosure.Serial = "NONE"`. Из-за этого:

- `Metrics` парсит строку обратно в `int64` (`metrics.go:38`);
- `SetLED` вынужден сравнивать с `"NONE"` (A3);
- невозможно отличить «датчик вернул ошибку» от «диск реально называется NONE».

Правильно: `Temperature *int64`, `Firmware, Serial, Map *string` (или маленький
`type Optional[T]`), а `ERR`/`N/A`/`NONE` печатать только в слое вывода
(`internal/cli`). Функции `readText`, `serial`, `temperature` должны возвращать
`(T, bool)` или `(T, error)`, а не глотать ошибку.

### C2. Позиционные литералы структур
`jbod.go:87` — `Enclosure{slot, device, field(...), field(...), field(...), field(...)}`,
`jbod.go:255` — `Fan{enc.Slot, enc.Serial, ..., comment, n}`. Шесть полей подряд,
пять из них строки. Перестановка `Vendor`/`Model` компилируется молча,
`go vet composites` внутри пакета не срабатывает. Везде использовать
именованные поля.

### C3. Пути sysfs протекают в доменную модель
`Disk.Locate` и `Disk.Fault` — это абсолютные пути к файлам, живущие в структуре,
которая иначе описывает железо, и которая сериализуется в вывод CLI. Плюс `SetLED` —
пакетная функция, принимающая `[]Disk`, хотя логически это операция клиента.

Лучше: `func (c *Client) SetLED(ctx, device string, kind LEDKind, on bool) error`,
которая сама резолвит путь от `c.Sysfs`. `LEDKind` — типизированная константа
вместо `string` с рантайм-проверкой `kind != "locate" && kind != "fault"`.

### C4. `details bool` — флаг-параметр
`Disks(ctx, enclosures, true)`. На месте вызова непонятно, что значит `true`
(`cli.go:82` vs `cli.go:127`). Либо `DiskOptions{WithTelemetry: true}`, либо
два метода.

### C5. CLI зависит от `*jbod.Client`, а не от интерфейса
Поэтому `cli_test.go` подменяет `Client.Run` — то есть тест CLI знает про
exec-слой и формат вывода `lsscsi`. Ввести

```go
type Inventory interface {
    Enclosures(context.Context) ([]Enclosure, error)
    Disks(context.Context, []Enclosure, DiskOptions) ([]Disk, error)
    Fans(context.Context, []Enclosure) ([]Fan, error)
}
```

и принимать её в `cli.Run`. Тесты CLI станут проверять форматирование, а не парсинг.

### C6. `Client` мутабелен и используется конкурентно
`Client.Run` — публичное поле, которое HTTP-хендлер читает из нескольких горутин,
а тесты перезаписывают на лету (`jbod_test.go:113`). Гонки сейчас нет только
потому, что запись происходит между запросами. Сделать поля приватными,
конструктор `New(opts ...Option)` и `WithRunner(...)` для тестов.

### C7. `Client{Sysfs: ""}` — рабочее состояние
`cli_test.go:13` создаёт клиент без `Sysfs`, и `filepath.Join("", slot)` даёт
относительный путь. Тест проходит случайно. `New()` должен быть единственным
входом, а пустой `Sysfs` — ошибкой или подставлять дефолт.

---

## D. Структура пакетов

### D1. `internal/jbod` делает четыре вещи
Сбор данных, доступ к sysfs, управление LED, кодирование метрик **и** HTTP-хендлер.
Разделить:

```
internal/jbod      — доменные типы + сбор (exec, sysfs)
internal/jbod/parse.go — чистые парсеры (см. D3)
internal/metrics   — кодирование в text format 0.0.4
internal/exporter  — http.Handler, таймауты, singleflight
internal/cli       — разбор аргументов
internal/cli/output.go — tabwriter-рендеринг
```

### D2. `cli.Run` — switch на 110 строк
Одна функция парсит аргументы, оркестрирует сбор и рендерит таблицы для трёх
подкоманд. Разнести на `cmdList`, `cmdLED`, `cmdPrometheus` (по файлу),
рендеринг — в отдельный `output.go`.

### D3. Парсеры не выделены и почти не тестируются напрямую
`field`, `temperature`, `serial`, `fanLine`/`rpm`, разбор `sg_map`, разбор
`lsscsi` и извлечение `comment` (`jbod.go:246`, `SplitN(l, ",", 3)` — самое
хрупкое место) размазаны по методам, делающим ещё и exec. Собрать в `parse.go`
как чистые функции `[]byte → struct, error` и покрыть таблично + фаззингом.

### D4. Мелочи репозитория
- `module jbod-go` — неканоничный путь; сделать `github.com/<user>/jbod-go`,
  иначе `go install` и импорт снаружи невозможны.
- Пустой каталог `jbod-go/` внутри репозитория — убрать.
- `.idea/` (`modules.xml`, `jbod-go.iml`) закоммичен; добавить в `.gitignore`.
- `cmd/jbod/main.go` и `cmd/prometheus-jbod-exporter/main.go` идентичны, кроме
  одного вызова — вынести общий `internal/cli.Main(fn)`.

---

## E. Разбор аргументов

`internal/cli` содержит три обходных приёма:

1. `boolean()` регистрирует один указатель под коротким и длинным именем;
2. ручное разворачивание `-ed` в `-e -d` с зашитым алфавитом `"edf"` (`cli.go:60`) —
   работает только для `list`, ломается на любом новом флаге;
3. спецкейс позиционных `IP PORT` в `Exporter` (`cli.go:171`) и тройная
   регистрация `-i`/`--ip`/`--ip-address`, где «последний выигрывает» молча.

Два разумных пути:

- **Оставить stdlib** (принцип «ноль зависимостей»): собрать всё в один
  документированный `expandShortFlags(alphabet string, args []string)` с
  таблицей тестов, включая `-ef`, `-e -d`, `--`, `-e=false`.
- **Взять `spf13/pflag`**: одна маленькая зависимость даёт настоящую POSIX-семантику
  (группировка коротких, `--flag=value`, `--`), и все три костыля исчезают.
  Плюс совместимость с clap станет буквальной, а не «на глаз».

---

## F. Версия Go и идиомы

- `go 1.22` в `go.mod` — вне поддержки; поднять до 1.25 (CI тоже на `1.22.x`).
- После этого: `slices.SortFunc` вместо `sort.Slice`, `errors.Join` для
  частичных ошибок (B5), `cmp.Or` вместо цепочек fallback, `for i := range n`,
  `strings.Lines` вместо `strings.Split(s, "\n")` в парсерах.
- **Логирования нет вообще.** Демон, который ходит в железо, не пишет ни строчки,
  кроме `==> Started on ...`. Добавить `log/slog`: JSON в systemd, текст в CLI;
  логировать preflight, длительность scrape, каждую упавшую команду.
- Версия зашита дважды: `"jbod-go 1.0.0"` (`cli.go:49`) и `Version: 1.0.0`
  (`debian/control`). Перейти на `-ldflags -X` + `runtime/debug.ReadBuildInfo()`
  как fallback.
- Все экспортируемые идентификаторы (`Client`, `Runner`, `Enclosure`, `Disk`,
  `Fan`, `SetLED`, `Help`) без doc-комментариев — `go doc` пустой.

---

## G. Тесты

Что уже хорошо: временное дерево sysfs, подстановка `Run`, проверка
исчезновения устройств, HTTP-коды, битый VPD.

Чего не хватает:

- **Субтесты и `t.Parallel()`.** Сейчас это четыре монолитных функции;
  при падении `t.Fatal(ds)` печатает всю структуру вместо «поле X: got/want».
- **Итерация по `map`** в `TestMalformedAndUnavailable` и `TestMetricsHTTP`
  (`jbod_test.go:140`, `:125`) — недетерминированный порядок; заменить на срез структур.
- **Golden-файлы** для вывода `list` и для `/metrics` (с флагом `-update`).
  Сейчас проверяется `strings.Contains`, то есть лишние/пропавшие строки не ловятся.
- **Фаззинг** `serial()` и `temperature()` — обе разбирают недоверенный бинарный
  и текстовый ввод от железа, это ровно тот случай, где `go test -fuzz` дешёв.
- **Регрессия на A1** (`list -e -f`) и на A2 (порядок `Slot 2` / `Slot 10`).
- **Конкурентность**: тест, что параллельные scrape не удваивают опрос (после B2),
  и что SIGTERM прерывает scrape (после B3).
- `cmd/*` — 0% покрытия; после D4 (общий `Main`) появится что тестировать.

---

## H. Сборка, CI, пакет

### CI (`.github/workflows/go.yml`, 13 строк)
Добавить: `gofmt -l . | tee /dev/stderr | wc -l` как гейт, `golangci-lint`,
`govulncheck`, матрицу `GOOS/GOARCH` (linux amd64+arm64), матрицу версий Go,
`actions/cache` для модулей, `-covermode=atomic` с публикацией отчёта.
Отдельный release-workflow (goreleaser) со сборкой `.deb` и checksums.

### Makefile
- `VERSION ?= $(shell git describe --tags --always --dirty)` и
  `-ldflags "-s -w -X main.version=$(VERSION)"`; `CGO_ENABLED=0`.
- Цели `fmt`, `lint`, `cover`.
- `deb: build` затем `$(MAKE) install`, который зависит от `build` — сборка идёт дважды.

### systemd unit — нет ни одной директивы усиления
```ini
Type=exec
EnvironmentFile=-/etc/default/prometheus-jbod-exporter
ExecStart=/usr/bin/prometheus-jbod-exporter $ARGS
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6
RestrictNamespaces=yes
MemoryDenyWriteExecute=yes
SystemCallFilter=@system-service
CapabilityBoundingSet=CAP_SYS_RAWIO CAP_DAC_OVERRIDE
```
`EnvironmentFile` заодно решает то, что сейчас адрес и порт вообще нельзя
изменить без правки unit-файла.

### debian/control
Version зашит (`1.0.0`) и разъезжается с `cli.go`; `Maintainer` — `example.invalid`;
нет `debian/changelog`, `md5sums`, `postinst` с `deb-systemd-helper`.
Есть `Conflicts: jbod` — стоит добавить `Provides: jbod` / `Replaces: jbod`,
раз бинарник называется так же. Проверить, что `scsi_temperature` действительно
приходит из `sg3-utils` в целевом релизе Debian, иначе зависимость неполна.

---

## I. Документация

- README только на русском, апстрим — на английском. Если форк планируется
  публиковать, нужен `README.en.md` (или наоборот).
- Раздел «Отличия от Rust-версии» полезный и честный; стоит дописать туда
  A9 (пропавшие `process_*`) — это единственное отличие, ломающее совместимость
  с существующими дашбордами.
- Нет `CHANGELOG.md`, `CONTRIBUTING.md`, doc-комментариев к пакетам.

---

## Порядок работ

1. **A1, A2, A3, A8** — быстрые правки с тестами (полдня).
2. **B1 + B2 + B3** — параллельный сбор, singleflight, BaseContext. Именно это
   определяет, будет ли экспортёр работать на реальной полке (1–2 дня).
3. **C1 + C2 + C3** — типы: `*int64`/`Optional`, именованные литералы, LED как
   метод. Затрагивает почти все файлы, поэтому после того, как тесты укреплены (1 день).
4. **A4, A5, B5, F (slog)** — эксплуатационная диагностика (1 день).
5. **D1, D2, D3, C5** — разделение пакетов и интерфейс `Inventory` (1–2 дня).
6. **E** — решение по `pflag` vs собственный парсер, затем чистка CLI.
7. **G, H, I** — тесты, CI, systemd, пакет, документация.
