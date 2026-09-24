# jbod-go

Перенос [Gandi/jbod-rs](https://github.com/Gandi/jbod-rs) на Go.
Исходная ревизия: `54fb20260aa0d5c88855fb71f3b9b7faf2d21e13`.
Лицензия BSD-2-Clause; исходные уведомления сохранены в LICENSE.

English version: [README.en.md](README.en.md).

## Требования и сборка

Go 1.25+ и две зависимости: [prometheus/client_golang](https://github.com/prometheus/client_golang)
— официальный клиент Prometheus, которым экспортёр отдаёт метрики, — и
[spf13/pflag](https://github.com/spf13/pflag) для POSIX-разбора флагов.
Работа с оборудованием требует Linux,
драйвера enclosure, доступного /sys/class/enclosure и утилит
lsscsi, sg_inq, sg_map, sg_ses, sginfo, scsi_temperature.
На Debian/Ubuntu установите пакеты lsscsi и sg3-utils.
Чтение SAS-линков (`jbod phy`) обходится `/sys/class/sas_phy` и никаких
дополнительных пакетов не требует; SMP-половина (`jbod phy --smp`) нужна
только ей самой и требует smp_utils, поэтому в обязательные зависимости
этот пакет не входит и preflight его не ищет.
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
./bin/jbod health
./bin/jbod health naa.50050cc10c400000 --json
./bin/jbod sensors
./bin/jbod list --components naa.50050cc10c400000
./bin/jbod phy
./bin/jbod phy naa.50050cc10c400000 --json
sudo ./bin/jbod phy naa.50050cc10c400000 --smp
sudo ./bin/jbod led --locate /dev/sda --on
sudo ./bin/jbod led --locate /dev/sda --off
sudo ./bin/jbod led --fault /dev/sg1 --on
sudo ./bin/jbod led --enclosure naa.50050cc10c400000 --locate 5 --on
./bin/jbod prometheus --ip-address 127.0.0.1 --port 9945
```

Флаги `-e`, `-d`, `-f`, `-s` и `-c` независимы: каждый добавляет свою секцию вывода,
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
| `empty` | устройства нет, и корпус явно сообщает `not installed` |
| `unavailable` | всё остальное: слот не удалось прочитать, либо устройства нет и корпус не сказал, что корзина пуста |

Правило намеренно строгое. Шасси с двумя IOM (проверено на WD H4060-J)
регистрирует по одному sysfs-корпусу на модуль, каждый перечисляет все 60
корзин, и те 30, которыми он не владеет, отдаёт со статусом, для которого у
драйвера нет имени: `enclosure.c` индексирует таблицу названий сырым кодом
SES, а код 8 «no access allowed» лежит за её концом, и в sysfs получается
литеральное `(null)`. Если считать «устройства нет и объяснения нет» пустой
корзиной, 30 занятых слотов превращаются в 30 пустых — ровно та путаница,
ради которой три состояния и заведены. Причина печатается один раз сноской
под таблицей, а не колонкой в каждой строке.

Нечитаемый слот никогда не выдаётся за пустой: колонка `-` означает
«корпус не отдаёт такой атрибут», а не «ноль». В JSON это `null`.

Колонка FAULT разделена на две половины, как их кодирует SES: драйвер
кладёт в sysfs-атрибут `(status[3] & 0x60) >> 5`, где бит 6 — FAULT SENSED
(корпус сам обнаружил неисправность), а бит 5 — RQST FAULT (кто-то зажёг
индикатор). Поэтому `sensed` — это авария, `requested` — метка оператора, и
смешивать их нельзя. Запись выставляет только `requested`, и readback
сравнивает именно его.

Корпус выбирается любым из четырёх написаний, которые печатают сами
таблицы, — логическим идентификатором, серийным номером, SCSI-адресом или
generic-устройством:

```sh
jbod list -e 0x5000ccab05629d00       # позиционно
jbod list -e --enclosure-id /dev/sg2  # то же самое флагом
jbod list --slots 1:0:31:0            # только этот путь
jbod list 0x5000ccab05629d00          # секция не указана — покажет корпуса
jbod capabilities /dev/sg33
```

Имя корпуса — единственный позиционный аргумент у `list` и `capabilities`,
поэтому его можно писать без флага. Если указан только корпус и ни одной
секции, подразумевается `--enclosure`.

Один идентификатор может относиться к двум sysfs-корпусам: он опознаёт
шасси, а не модуль. Из четырёх написаний только generic-устройство
(`/dev/sg2` против `/dev/sg33`) различает модули — идентификатор и серийный
номер у них общие. В таком случае заголовок таблицы это говорит
(`same chassis as 1:0:31:0`), а `led` выбирает тот путь, который владеет
корзиной, — через второй модуль запись была бы принята и ничего не зажгла.

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
В строке результата печатается, куда именно ушла запись
(`[1:0:31:0 slot 30, /dev/sg34]`).

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

## Здоровье корпуса, компоненты и датчики

`jbod health` отвечает на вопрос «что с полкой», `jbod sensors` показывает
числа, на которых этот ответ основан, а `jbod list --components` перечисляет
каждый элемент SES, который корпус объявляет: корзины, блоки питания,
вентиляторы, датчики и модули ввода-вывода.

За один проход читаются четыре страницы: Configuration (`--page=cf`),
Enclosure Status (`--page=es`), join Enclosure Status + Element Descriptor +
Additional Element Status (`--join`) и Threshold In (`--page=th --raw`). Ничего не
пишется: чтение порогов — это чтение, а изменение порогов и охлаждения —
версия 1.4.

```
$ jbod health
Enclosure 1:0:0:0  address 0x5000ccab05629d00 (stable)  health critical
SCOPE       LEVEL     DETAIL
hardware    warning   INVOP=0 INFO=0 NON-CRIT=1 CRIT=0 UNRECOV=0
components  critical  8 elements: 4 ok, 1 critical, 3 unknown
collection  complete  3/4 pages read, generation 0x1
note: the threshold in page did not answer and is not required: sg_ses: Threshold In dpage not supported
note: 1 element(s) are declared by the configuration page and were not reported by any status page
```

Три строки отвечают на три разных вопроса и намеренно не сводятся в одну:

| Строка | Что это |
| --- | --- |
| `hardware` | вердикт самого корпуса: пять бит страницы Enclosure Status |
| `components` | худшее состояние среди элементов |
| `collection` | полнота опроса: сколько страниц ответило и совпал ли generation code |

Аппаратная авария и неудавшийся опрос — разные события, и смешивать их
нельзя: полка, у которой не ответила страница, не становится исправной, а
полка, у которой не реализована страница порогов, не становится сломанной.
Поэтому страница, которая не ответила, попадает в строку `collection` и в
сноски, а не в `hardware`, и каждый бит, который не был прочитан,
печатается как `-`, а не как `0`.

Уровни состояния:

| Уровень | Когда |
| --- | --- |
| `ok` | корпус сообщает `OK` |
| `warning` | SES `noncritical` |
| `critical` | SES `critical` |
| `unrecoverable` | SES `unrecoverable` |
| `absent` | `not installed`: элемент объявлен, и его нет |
| `unknown` | `unsupported`, `unknown`, `not available`, `no access allowed`, либо состояние не прочитано |

`unknown` и `absent` не участвуют в вычислении худшего уровня, но считаются
отдельно. Иначе шасси с двумя IOM всегда сообщало бы `unknown`: тридцать
корзин чужого модуля отдаются кодом «no access allowed», и это не диагноз
полки, а граница видимости модуля. Если же не прочитано вообще ничего,
ответ — `unknown`: тогда наблюдения нет.

Элемент, который объявлен в Configuration и не отдан ни одной status-страницей,
остаётся в списке со статусом `declared only`. «Полка сообщает о двух блоках
питания, ответил один» — это находка, а таблица с одним блоком питания её
прячет.

```
$ jbod sensors
Enclosure 1:0:0:0  address 0x5000ccab05629d00 (stable)
ID   NAME        TYPE                READING      VALUE  UNIT     STATUS       HEALTH   HIGH CRIT  HIGH WARN  LOW WARN  LOW CRIT
1,0  PSU A       power supply        temperature  41     celsius  OK           ok       -          -          -         -
3,0  TEMP IOM A  temperature sensor  temperature  35     celsius  OK           ok       65         60         0         -19
3,1  TEMP IOM B  temperature sensor  temperature  -      celsius  Unsupported  unknown  -          -          -         -
```

Пороги — это числа самого корпуса со страницы Threshold In, а не константа в
правиле алертинга: на следующей полке она будет другой. Порог температуры —
в градусах. Порог напряжения и тока страница задаёт в процентах от
номинала датчика — верхние выше номинала, нижние ниже, — а сам номинал ни на
одной странице не приходит, поэтому в таблице такой порог печатается с `%`.

Биты статуса элемента — `predicted_failure`, `fault_sensed`, `ident`,
`do_not_remove`, `swap`, у блока питания `ac_fail`, `dc_fail`,
`overtemp_warning`, `dc_overcurrent` и остальные — публикуются в двух
видах. `jbod_component_flag` — по элементу, только установленные биты:
почти все биты почти всегда нулевые (на H4060-J из 1961 бита модуля
установлены 25, и все это нормальные состояния), а по нулю на каждый было
3922 серии на хост, которые ничего не говорили. Сброшенный бит у элемента,
который есть в `jbod_component_info`, — это ноль, а не «не прочитано».
`jbod_enclosure_component_flags` — по корпусу, типу и биту: сколько
элементов его держат, нули тоже. Эта серия есть всегда, поэтому на ней и
пишется алерт: `jbod_enclosure_component_flags{flag="predicted_failure"} > 0`,
или «кабелей стало меньше» —
`delta(jbod_enclosure_component_flags{type="sas connector",flag="mated"}[10m]) < 0`;
`jbod_component_flag` затем говорит, какой именно элемент. `hot_swap` не
публикуется вовсе: это не состояние, а то, что элемент умеет, и он
установлен у каждого блока питания, вентилятора и модуля. `report` —
модуль, который сейчас отвечает на запросы. Имя бита берётся из полей SES-3,
а не из написания sg_ses: оно различается между типами элементов и версиями
(«Fault reqstd» и «Fault requested» — один бит). Поле, которого словарь не
знает, в метрику не попадает: так же отсекаются числа в той же форме
«Имя=N» — «Actual speed=0 rpm», «Time until power cycle=1». Элемент со
статусом «No access allowed» битов не получает: это половина корзин полки с
двумя IOM, и за неё отвечает другой модуль.

Страница читается сырой (`--raw`) и декодируется по странице Configuration,
а не по тексту sg_ses. Текст раскладывает пороги по строкам под заголовком
«Element N descriptor:», по-разному в разных версиях, а начиная с
sg3-utils 1.48 sg_ses пропускает типы элементов без порогов, не перешагивая
их дескрипторы: на любой полке, где корзины идут раньше датчиков, пороги
датчика в его выводе — это байты другого элемента. Сырая страница одинакова
во всех версиях: код генерации и по четыре байта на каждый элемент каждого
типа. Страница, в которой дескрипторов не столько, сколько объявляет
конфигурация, или код генерации другой, не декодируется вовсе — ошибка с
обоими числами, заметка в выводе и `jbod_scrape_errors_total`. Поле 00h по
SES значит «порог не поддерживается» и остаётся `-`. Датчик, который
объявляет показание и не отдал значения, остаётся строкой со значением `-`:
пропавшая строка неотличима от датчика, которого никогда не было, а ноль —
это ложь. В JSON каждое показание несёт значение, единицу, источник, время
опроса и причину отсутствия.

```
$ jbod list --components
Enclosure 1:0:0:0  address 0x5000ccab05629d00 (stable)
ID   TYPE                NAME        STATUS             HEALTH    READINGS  SLOT  SAS ADDRESS         DEVICE    MAP
0,0  array device slot   SLOT 00     OK                 ok        -         0     0x5000cca2a0d6e2f5  /dev/sg1  /dev/sda
0,1  array device slot   SLOT 01     No access allowed  unknown   -         1     -                   -         -
0,2  array device slot   SLOT 02     declared only      unknown   -         -     -                   -         -
1,1  power supply        PSU B       Critical           critical  -         -     -                   -         -
2,0  cooling             FAN ENCL 1  OK                 ok        7220 rpm  -     -                   -         -
```

Колонки SAS ADDRESS, DEVICE и MAP — это отображение slot → SAS address →
disk: адрес приходит из Additional Element Status, диск — из обхода sysfs, а
связывает их номер корзины, который отдаёт сам корпус (при его отсутствии —
номер элемента, затем имя компонента). Нулевой адрес не публикуется: это не
идентичность, и по нему все пустые корзины сошлись бы в одну.

Generation code читается со всех страниц. Если они не совпали, страницы
описывают разные конфигурации: отчёт помечается как смешанный и `collection`
становится `partial`. Повторного опроса нет — полка, которую в этот момент
перенастраивают, повторялась бы бесконечно, — вместо этого сказано прямо,
что произошло.

`--json` есть у `health`, `sensors` и `list --components`. Команды ничего не
решают за оператора: `jbod health` печатает состояние и всегда завершается
с кодом 0, если сам сбор не сломался.

## SAS-соединения и счётчики ошибок

`jbod phy` показывает не полку, а линки, по которым она подключена: SAS-адрес
каждого phy, скорость, на которой согласовался линк, состояние и счётчики
ошибок, которые ведёт само железо. Это ответ на ситуацию, которую `health` и
`sensors` показать не могут: все элементы корпуса отвечают `OK`, а диски
отваливаются по таймауту — это кабель, и виден он только здесь.

```
$ jbod phy 0x5000ccab05629d00
Host 1  3 phys: 2 up, 1 disabled  (enclosures 1:0:0:0)
PHY        PORT      TYPE           SAS ADDRESS         ID  STATE     NEGOTIATED    MAX        INV DW  DISP  SYNC  RESET
phy-1:0    port-1:0  end device     0x500605b00b1e2f40  0   up        12.0 Gbit     12.0 Gbit  0       0     0     0
phy-1:1    port-1:0  end device     0x500605b00b1e2f41  1   up        6.0 Gbit      12.0 Gbit  1274    7     31    2
phy-1:0:0  -         edge expander  0x5000ccab05629d3f  0   disabled  Phy disabled  -          -       -     -     -

EXPANDER      SAS ADDRESS         IDENTITY           LEVEL  PHYS  SMP DEVICE
expander-1:0  0x5000ccab05629d3f  HGST H4060-J 4013  1      -     /dev/bsg/expander-1:0
note: these are the phys of host 1, the HBA the listed enclosures are attached through; which phy carries which shelf is topology and is not reported here
```

Четыре последние колонки — стандартные счётчики ошибок линка SAS: невалидные
dword, ошибки running disparity, потери синхронизации dword и неудавшиеся
сбросы phy. Первая растёт на плохом кабеле или разъёме, третья — на линке,
который падает и поднимается; вторая строка примера — это именно такой линк,
который при этом отвечает и числится `up`.

Абсолютное значение счётчика само по себе диагнозом не является. На реальной
полке H4060-J почти все линки 12 Гбит/с показывают порядка 60-75 невалидных
dword и столько же ошибок disparity при двух потерях синхронизации — одинаково
на всех шести экспандерах. Это шум согласования линка при поднятии, а не
шесть сотен плохих кабелей. Интересен не уровень, а рост: в Prometheus это
`rate()`, в CLI — два прогона и разница между ними.

Источников два, и они стоят по-разному:

| Источник | Что даёт | Чего стоит |
| --- | --- | --- |
| `/sys/class/sas_phy` | адрес, скорость, состояние и четыре счётчика каждого phy | ничего: это чтение атрибутов, внешних команд нет |
| SMP, флаг `--smp` | то же для phy экспандера, плюс адрес на другом конце линка | по одному `smp_rep_phy_err_log` на phy, нужен smp_utils |

Поэтому SMP включается флагом, а не по умолчанию: шестьдесят восемь phy
экспандера — это шестьдесят восемь запросов через один SMP-процессор. Для
команды, которую набрал оператор, это нормально; для scrape раз в пятнадцать
секунд — нет, и экспортёр SMP не использует вообще.

Список phy экспандера строится по числу, которое отдаёт сам экспандер
(`smp_rep_general`), а `smp_discover --multiple` накладывается сверху. Это не
педантичность: на WD H4060-J discover описал 24 phy из 49, и если считать его
вывод списком phy, то у остальных 25 журнал ошибок не спросят вообще. Phy,
которого discover не описал, получает строку с прочитанными счётчиками и
причиной, по которой у него нет адреса на другом конце.

Колонка ROUTING — это буква, которой `smp_discover` обозначает routing
attribute: `D` direct, `S` subtractive, `T` table. Букву, которой в этом
списке нет, jbod-go передаёт как есть — на H4060-J так приходит `U` для
146 phy из 148, и придумывать ей значение незачем.

Состояние `vacant` приходит только по SMP: экспандер объявляет phy в своём
счётчике и сообщает, что его нет. Такие phy не получают строку в таблице —
только сноску с диапазонами номеров: строка у них не может содержать ничего,
кроме прочерков, а на H4060-J это 192 строки из 370. В JSON они остаются. У такого phy журнал ошибок не спрашивается
— экспандер уже ответил, а запрос стоил бы по одному SMP на phy (на H4060-J
это 24 из 49) и вернул бы ошибку с известной заранее причиной. В транспорте
sysfs написания для `vacant` нет, поэтому тот же phy в верхней таблице
читается как `unknown`.

Счётчики только читаются. У `smp_rep_phy_err_log` есть опция `--zero`,
которая обнуляет то, что печатает; jbod-go её не передаёт никогда — сбор
диагностики, уничтожающий историю подозрительного кабеля, хуже отсутствия
диагностики.

Чего этот отчёт не утверждает:

- **`unknown` — это не «линк лежит».** Транспорт печатает `Unknown` и для
  пустого разъёма, и для поля, которое драйвер не заполнил; различить их
  средствами sysfs нечем, поэтому в колонке стоит `unknown`, а не `down`.
  Отдельно существуют `disabled` (так сказал транспорт) и `failed`
  (согласование скорости не удалось) — это диагнозы, и они не сводятся
  к «не up».
- **Счётчик, которого нет, печатается как `-`.** Драйвер, не публикующий
  счётчики ошибок, не сообщил, что линк чистый. Ноль сказал бы именно это.
- **Phy принадлежит HBA, а не полке.** Имя корпуса сужает отчёт до его хоста,
  и сноска под таблицей говорит прямо: связать конкретный phy с конкретной
  полкой — это топология, то есть следующие пункты версии 1.3.

`capabilities` отвечает на тот же вопрос заранее: `sas.phy`,
`sas.phy_error_counters` и `smp.phy_error_counters` — с доказательством,
сколько phy у этого хоста, сколько из них публикуют счётчики и есть ли
bsg-узел, которому можно адресовать SMP. Ни одна из трёх никогда не сообщает
поддержку записи: скорость и состояние — это отчёт о том, что сделал линк,
а не настройка.

`--json` есть и здесь. Отсутствующий счётчик — это `null` с причиной рядом,
а не ноль.

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
| `--log-level` | info | debug, info, warn или error |
| `--log-format` | json | json для systemd, text для терминала |

Внешние команды выполняются параллельно с ограничением `--concurrency`:
на полку в 60 дисков приходится 120 запусков процессов, последовательно они
не укладываются в интервал scrape. Порядок вывода от параллелизма не зависит.

С 1.2 к этому добавляются страницы SES: до четырёх вызовов `sg_ses` на
корпус за проход — по корпусам параллельно, внутри корпуса последовательно
(страницы идут через один и тот же SES-процессор, и ставить их в очередь
одновременно незачем). Страница порогов читается только если корпус
сообщает о датчиках.

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

Добавлены в 1.1:

| Метрика | Тип | Labels | Значение |
| --- | --- | --- | --- |
| jbod_enclosure_info | gauge | enclosure, enclosure_id, id_source, vendor, model, revision, serial | идентичность корпуса, всегда 1 |
| jbod_enclosure_slots | gauge | enclosure, enclosure_id, occupancy | число слотов в каждом состоянии |
| jbod_fan_speed_rpm | gauge | enclosure, enclosure_id, component, component_id | RPM вентилятора без коллизий |

Добавлены в 1.2:

| Метрика | Тип | Labels | Значение |
| --- | --- | --- | --- |
| jbod_enclosure_health | gauge | enclosure, enclosure_id, source, level | 1 у текущего уровня; source — `hardware` или `components` |
| jbod_enclosure_components | gauge | enclosure, enclosure_id, type, health | сколько элементов каждого типа в каждом состоянии |
| jbod_component_info | gauge | enclosure, enclosure_id, component, component_id, type, status, health | один элемент, всегда 1 |
| jbod_component_flag | gauge | enclosure, enclosure_id, component, component_id, type, flag | установленный бит статуса элемента; серия есть только пока бит установлен, значение всегда 1 |
| jbod_enclosure_component_flags | gauge | enclosure, enclosure_id, type, flag | сколько элементов типа держат бит, для каждого бита, который корпус сообщает; 0, если ни один |
| jbod_sensor_temperature_celsius | gauge | enclosure, enclosure_id, component, component_id, type | температура элемента корпуса |
| jbod_sensor_voltage_volts | gauge | те же | напряжение |
| jbod_sensor_current_amps | gauge | те же | ток |
| jbod_sensor_temperature_threshold_celsius | gauge | те же + threshold | порог температуры корпуса: high_critical, high_warning, low_warning, low_critical |
| jbod_sensor_voltage_threshold_percent | gauge | те же + threshold | порог напряжения в процентах от номинала: high_* выше него, low_* ниже |
| jbod_sensor_current_threshold_percent | gauge | те же + threshold | порог тока в процентах выше номинала: только high_critical и high_warning |
| jbod_slot_sas_address_info | gauge | enclosure, enclosure_id, slot, component_id, sas_address, device, block_device | отображение slot → SAS address → disk, всегда 1 |

Добавлено в 1.3:

| Метрика | Тип | Labels | Значение |
| --- | --- | --- | --- |
| jbod_sas_phy_info | gauge | host, phy, port, sas_address, device_type, negotiated_link_rate | один phy, всегда 1 |
| jbod_sas_phy_state | gauge | host, phy, port, sas_address, device_type, state | текущее состояние phy — up, disabled, failed, spin-up hold или unknown; одна серия на phy, всегда 1 |
| jbod_sas_device_phys | gauge | host, sas_address, device_type, state | сколько phy устройства — экспандера или HBA — в каждом состоянии; 0, если ни одного |
| jbod_sas_phy_negotiated_link_rate_gbps | gauge | host, phy, port, sas_address, device_type | скорость линка; серии нет, если скорости нет |
| jbod_sas_phy_invalid_dword_total | counter | те же | невалидные dword |
| jbod_sas_phy_running_disparity_error_total | counter | те же | ошибки running disparity |
| jbod_sas_phy_loss_of_dword_sync_total | counter | те же | потери синхронизации dword |
| jbod_sas_phy_reset_problem_total | counter | те же | неудавшиеся сбросы phy |
| jbod_sas_expander_phys_unanswered | gauge | host, sas_address | phy экспандера без скорости, журнал ошибок которых экспандер не отдал; своих серий они не получают |

Счётчики отдаются как counter со значением самого железа. Они обнуляются при
сбросе phy, перезагрузке драйвера и ребуте — и это ровно то, что Prometheus
умеет читать: `rate()` и `increase()` видят падение как reset и никогда не
дают отрицательный прирост. Накапливать их в экспортёре было бы неверно, он
сам перезапускается и прошлую сумму не помнит.

Labels — хост и phy, а не корпус: phy принадлежит HBA. Счётчик, которого
транспорт не отдал, серии не получает вовсе — ноль здесь означал бы чистый
линк. Экспортёр читает только sysfs: SMP стоит по запросу на phy и в scrape
не ходит.

Состояние phy публикуется в двух видах, как биты элементов.
`jbod_sas_phy_state` — одна серия на phy с его текущим состоянием и
значением 1: `disabled`, `failed` и `unknown` — три разных диагноза, и
«поднят или нет» складывал их в один ноль. Полный набор состояний говорил
то же самое четырьмя нулями на phy — 796 из 995 серий на хосте с H4060-J.
`jbod_sas_device_phys` — по каждому устройству, экспандеру или HBA, и
каждому состоянию: сколько его phy в нём, нули тоже. Когда линк меняет
состояние, его серия в `jbod_sas_phy_state` заканчивается и начинается
другая; счётчик по устройству есть всегда, поэтому алерт пишется на него —
`delta(jbod_sas_device_phys{state="up"}[10m]) < 0` («на экспандере стало
меньше поднятых линков»), а `jbod_sas_phy_state{state!="up"}` затем
показывает, какой phy. Вакантного состояния нет ни там, ни там: такой phy
серий не получает.

Phy экспандера, у которого нет скорости и все четыре счётчика существуют, но
не читаются, отдельных серий не получает. Драйвер отвечает на эти атрибуты,
спрашивая у экспандера журнал ошибок phy по SMP, и отказ по всем четырём — это
экспандер, который отказался описывать phy: так вакантный phy выглядит в
sysfs. На WD H4060-J это ровно те 192 phy из 370, которые SMP называет
вакантными, phy в phy; отключённые phy и phy без линка счётчики отдают. Вместо
192 пустых строк — одна серия на экспандер, `jbod_sas_expander_phys_unanswered`,
в том числе нулевая: экспандер, который перестал описывать phy, виден как
скачок.

Состояние — это label, а не число: числовой шкале пришлось бы куда-то
поместить `unknown`, и любое место неверно. Рядом с `ok` он прячет полку,
которую не удалось прочитать, рядом с `critical` — будит дежурного из-за
нереализованной страницы порогов. Серия публикуется для каждого уровня,
поэтому вернувшаяся в норму полка отдаёт ноль, а не оставляет висеть
прошлую критическую серию. Показание, которого корпус не отдал, не
публикуется вовсе.

Добавлены метрики состояния самого сбора:

| Метрика | Тип | Значение |
| --- | --- | --- |
| jbod_up | gauge | 1, если сбор завершился полностью |
| jbod_scrape_duration_seconds | gauge | длительность последнего сбора |
| jbod_scrape_errors_total | counter | накопленные ошибки по collector (enclosures, slots, disks, fans, components, sas, led) |
| jbod_snapshot_timestamp_seconds | gauge | когда начался сбор, стоящий за этим ответом |
| jbod_collection_complete | gauge | 1, если у корпуса ответили все обязательные страницы и совпал generation code |
| jbod_ses_page_read | gauge | ответила ли конкретная страница SES (labels: page, required) |
| jbod_enclosure_generation_changed | gauge | 1, если страницы одного прохода описали разные конфигурации |
| jbod_enclosure_components_missing | gauge | сколько объявленных элементов не отдала ни одна status-страница |

Метрики самого экспортёра приходят из client_golang и собираются заново на
каждый запрос, мимо кеша сбора:

| Метрика | Тип | Значение |
| --- | --- | --- |
| process_* | gauge/counter | CPU, память, дескрипторы и время старта процесса (только Linux: читается из /proc) |
| go_* | gauge/counter | горутины, GC и память рантайма |
| jbod_build_info | gauge | версия бинарника и тулчейн в labels, значение всегда 1 |
| promhttp_metric_handler_requests_total | counter | ответы /metrics по коду |

`jbod_enclosure_info` — точка join для всех серий, у которых в labels стоит
SCSI-адрес: адрес назначается при сканировании и меняется, поэтому дашборд,
которому нужна устойчивая идентичность, джойнит по `enclosure` и берёт
`enclosure_id`. Label `id_source` говорит, настоящий это идентификатор
(`logical`, `serial`) или снова адрес (`address`).

Метрики самого процесса (`process_cpu_seconds_total`,
`process_resident_memory_bytes`, `process_virtual_memory_bytes`,
`process_start_time_seconds`, `process_open_fds`, `process_max_fds`) отдаёт
сборщик из client_golang — тот же, что и в остальных экспортёрах Prometheus;
в Rust-версии их отдавал крейт prometheus, и дашборды по ним ломались бы без
них. Читаются они при каждом запросе и не кешируются. На системах без
`/proc` (например macOS) они просто не выводятся — лучше отсутствие серии,
чем нули.

Ответ отдаётся через `promhttp`, поэтому доступны согласование формата
(text 0.0.4 и OpenMetrics), gzip и заголовки, которые ждёт Prometheus:
ничего из этого в репозитории не написано руками.

### jbod_fan_rpm удалена

В `jbod_fan_rpm` label `device` был описанием вентилятора, а `slot` —
индексом sg_ses. Оба значения локальны для корпуса, поэтому «Fan A» с
индексом `2,0` совпадал по labels у каждой полки в стойке, и значение
последней затирало предыдущие. Исправленная метрика — новое имя:

```
jbod_fan_speed_rpm{enclosure="1:0:0:0",enclosure_id="naa.5000...01",component="Fan A",component_id="2,0"} 1200
jbod_fan_speed_rpm{enclosure="10:0:0:0",enclosure_id="naa.5000...02",component="Fan A",component_id="2,0"} 4800
```

`jbod_fan_rpm` удалена раньше обещанного 2.0: это восемь серий на хост,
которые не несли ничего сверх `jbod_fan_speed_rpm`, а на стойке из
нескольких полок — неверные числа. Дашборд или правило, которые её читают,
переводятся заменой имени и labels: `device` → `component`, `slot` →
`component_id`, плюс `enclosure` и `enclosure_id`. Флаг
`--deprecated-metrics` по-прежнему принимается, чтобы unit-файл, в котором
он записан, не перестал запускать экспортёр, но ничего не делает и
предупреждает об этом.

Каждый scrape собирает свежие значения. Исчезнувшие устройства удаляются
из выдачи. Недоступная температура пропускается (в CLI отображается ERR);
недоступная прошивка отображается как N/A.

Частичный сбор — это HTTP 200: сломанный датчик, недоступное дерево sysfs
одного корпуса, страница SES, которая не ответила, или вентилятор без RPM
учитываются в `jbod_scrape_errors_total` (страницы — под collector
`components`, а полнота опроса видна в `jbod_collection_complete`),
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
- Есть `health`, `sensors` и `list --components`: состояние корпуса и его
  элементов, датчики с порогами и отображение slot → SAS address → disk.
  Аппаратное состояние и полнота опроса — две разные строки отчёта.
- Требуется ровно одно состояние --on/--off. Неизвестные устройства — ошибка.
- Совместимость метрик полная, включая `process_*` (их, как и в Rust-версии,
  отдаёт официальный клиент Prometheus); сверх Rust-версии есть
  `jbod_up`, `jbod_scrape_duration_seconds`, `jbod_scrape_errors_total`,
  `jbod_enclosure_info`, `jbod_enclosure_slots`, `jbod_fan_speed_rpm`,
  метрики состояния и компонентов (`jbod_enclosure_health`,
  `jbod_enclosure_components`, `jbod_component_info`), датчики с порогами
  (`jbod_sensor_*`), отображение корзин (`jbod_slot_sas_address_info`) и
  полнота сбора (`jbod_collection_complete`, `jbod_ses_page_read`,
  `jbod_snapshot_timestamp_seconds`).

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
(`make vendor`) — зависимости (client_golang, pflag и их транзитивные
модули) в репозитории не лежат.

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

Страницы SES разбираются от фикстур: конфигурация, статус корпуса, join с
Additional Element Status и пороги, включая три написания типа элемента,
нулевой SAS-адрес, overall-элемент и датчик без значения.

Вывод `list`, `health`, `sensors` и текст метрик зафиксированы golden-файлами, поэтому лишняя или
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
  `sespage.go` — парсеры страниц SES, `ses.go` — модель компонентов,
  датчиков и состояния, `order.go` — натуральная сортировка слотов.
- internal/metrics — снимок в виде `prometheus.Collector`: дескрипторы серий
  и const-метрики, сам формат пишет client_golang.
- internal/exporter — HTTP-обработчик, таймаут scrape, объединение
  одновременных scrape, кеш по TTL и реестр с процессными, рантаймовыми и
  build-info метриками; ответ пишет `promhttp`.
- internal/cli — разбор аргументов: `list.go`, `led.go`, `prometheus.go`,
  `health.go` (health и sensors), вывод таблиц в `output.go`, версия в
  `version.go`, логгер в `log.go`.
