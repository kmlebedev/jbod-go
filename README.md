# jbod-go

Перенос [Gandi/jbod-rs](https://github.com/Gandi/jbod-rs) на Go.
Исходная ревизия: `54fb20260aa0d5c88855fb71f3b9b7faf2d21e13`.
Лицензия BSD-2-Clause; исходные уведомления сохранены в LICENSE.

## Требования и сборка

Go 1.22+, без сторонних Go-модулей. Работа с оборудованием требует Linux,
драйвера enclosure, доступного /sys/class/enclosure и утилит
lsscsi, sg_inq, sg_map, sg_ses, sginfo, scsi_temperature.
На Debian/Ubuntu установите пакеты lsscsi и sg3-utils.
Доступ к устройствам /dev/sg* и запись LED требуют соответствующих прав
(обычно root). Справка и тесты работают без оборудования, в том числе на macOS.

```sh
make build
./bin/jbod help
./bin/jbod list -e
./bin/jbod list -d
./bin/jbod list -ed
./bin/jbod list -f
./bin/jbod list -ef
sudo ./bin/jbod led --locate /dev/sda --on
sudo ./bin/jbod led --locate /dev/sda --off
sudo ./bin/jbod led --fault /dev/sg1 --on
./bin/jbod prometheus --ip-address 127.0.0.1 --port 9945
```

Флаги `-e`, `-d` и `-f` независимы: каждый добавляет свою секцию вывода,
поэтому `list -ef` печатает и корпуса, и вентиляторы. Диски упорядочены
натурально — `Slot 2` идёт перед `Slot 10`.

LED можно указывать несколько раз: `led -l /dev/sda -l /dev/sdb --on`.
Поддерживаются пути устройств /dev/sg* и соответствующие /dev/sd*.
Операции выполняются по очереди; ошибка останавливает выполнение,
предыдущие успешные записи не откатываются.

## Prometheus

Оба способа запуска используют один экспортёр:

```sh
./bin/jbod prometheus -i 0.0.0.0 -p 9945
./bin/prometheus-jbod-exporter 0.0.0.0 9945
```

Оба бинарника понимают `--help` и `--version`.

По умолчанию слушает 0.0.0.0:9945. GET / возвращает пустой ответ;
GET /metrics — Prometheus text format 0.0.4.

Сохранены имена и наборы labels:

| Метрика | Labels |
| --- | --- |
| number_of_enclosures | нет |
| jbod_slot_temperature | slot, enclosure |
| jbod_fan_rpm | device, slot |

Как в исходном проекте, device у вентилятора означает описание вентилятора,
а slot — индекс sg_ses. Если разные корпуса имеют одинаковые описания и
индексы, они совпадут по labels: последняя запись заменяет предыдущую.
Это ограничение схемы исходных метрик сохранено для совместимости.

Каждый scrape собирает свежие значения. Исчезнувшие устройства удаляются
из выдачи. Недоступная температура пропускается (в CLI отображается ERR);
недоступная прошивка отображается как N/A. Ошибка получения списка корпусов,
дисков или вентиляторов возвращает HTTP 503. Команда ограничена 15 секундами,
scrape — двумя минутами; для больших корпусов настройте scrape_timeout.

## Отличия от Rust-версии

- Экспортёр работает на переднем плане; SIGINT/SIGTERM корректно останавливают HTTP.
  Для фоновой работы используется systemd, без fork и второго дочернего процесса.
- Таблицы текстовые, без цветовых и мигающих ANSI-последовательностей.
- Утилиты находятся через PATH; справка не требует установленных SCSI-утилит.
- Ошибки возвращаются с ненулевым кодом вместо panic или молчаливого успеха.
- VPD page 0x80 разбирается с учётом бинарного заголовка и длины.
- Слоты определяются по наличию device/scsi_generic, включая нестандартные имена.
- LED не зависит от доступности scsi_temperature и sginfo.
- Требуется ровно одно состояние --on/--off. Неизвестные устройства — ошибка.

## Установка и Debian

```sh
sudo make install
sudo systemctl daemon-reload
sudo systemctl enable --now prometheus-jbod-exporter
```

`make install` ставит оба бинарника в /usr/bin и unit в /lib/systemd/system.
Для установки в staging-каталог задайте DESTDIR. При изменении PREFIX
скорректируйте ExecStart в unit-файле.

На целевой Linux-системе с dpkg можно собрать пакет: `make deb`.
Результат: dist/jbod-go.deb. Перед распространением замените Maintainer в
debian/control на свои данные. Сборка Debian-пакета на macOS не проверялась.

## Проверка

```sh
make test
GOOS=linux GOARCH=amd64 go build ./...
GOOS=linux GOARCH=arm64 go build ./...
```

Тесты используют временное дерево sysfs и подставные ответы SCSI-утилит:
обнаружение оборудования, VPD, температура, RPM, управление LED,
совместимые метрики, исчезновение устройств, HTTP-ошибки и CLI.

На реальном JBOD проверки не выполнялись. Перед эксплуатацией проверьте
вывод list и метрики на своём оборудовании; тестовые ответы не заменяют
проверку разных версий sg3-utils и моделей корпусов.

Структура:
- cmd/jbod — CLI.
- cmd/prometheus-jbod-exporter — отдельный entry point экспортёра.
- internal/jbod — получение данных, sysfs, LED, HTTP и метрики.
- internal/cli — аргументы команд и вывод.
