# jbod-go

A Go port of [Gandi/jbod-rs](https://github.com/Gandi/jbod-rs).
Ported from revision `54fb20260aa0d5c88855fb71f3b9b7faf2d21e13`.
BSD-2-Clause; the original notices are kept in LICENSE.

Russian version: [README.md](README.md).

## Requirements and building

Go 1.25+ and one dependency, [spf13/pflag](https://github.com/spf13/pflag),
for POSIX option parsing. Talking to hardware needs Linux, the enclosure
driver, a readable /sys/class/enclosure and the lsscsi, sg_inq, sg_map,
sg_ses, sginfo and scsi_temperature tools. On Debian and Ubuntu install the
lsscsi and sg3-utils packages. Reading /dev/sg* and writing LEDs need the
matching privileges (usually root). Help and the test suite work without any
hardware, macOS included.

Both binaries run a preflight check first: they verify that every tool is
present and that /sys/class/enclosure is readable, and report everything
that is missing as one list, with the package names. Tools are resolved once
to absolute paths against a fixed `PATH=/usr/sbin:/usr/bin:/sbin:/bin` and
run with `LC_ALL=C` — the parsers depend on the English output of sg3-utils,
and a daemon running as root should not depend on an inherited environment.

The module is `github.com/kmlebedev/jbod-go`, so the binaries can be
installed without cloning:

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

`-e`, `-d`, `-f` and `-s` are independent: each adds its own section, so
`list -ef` prints both the enclosures and the fans. Disks are ordered
naturally — `Slot 2` comes before `Slot 10`.

Option parsing is POSIX: short flags group (`-ed`, `-edf`), long flags take
`--flag=value`, `--` ends the options, and every subcommand has `--help`.
For the exporter `--ip` and `--ip-address` are one option under two
spellings, not two flags where the last one silently wins.

LEDs can be given more than once: `led -l /dev/sda -l /dev/sdb --on`. Both
/dev/sg* paths and the matching /dev/sd* names work. Writes happen in order
and stop at the first failure; the ones that already succeeded are not rolled
back.

## Slots, addressing and capabilities

A slot and a disk are different things. The walk starts from the enclosure's
components rather than from `device/scsi_generic`, so an empty bay exists in
the model and shows up in the output:

```
$ jbod list --slots
Enclosure 1:0:0:0  id naa.50050cc10c400000 (logical)
SLOT  NAME            TYPE          STATUS         OCCUPANCY    DEVICE    MAP       LOCATE  FAULT             POWER
1     Slot 01, front  array device  OK             occupied     /dev/sg1  /dev/sda  off     off               on
2     Slot 02, front  array device  not installed  empty        -         -         on      requested         on
3     Slot 03, front  array device  unavailable    unavailable  -         -         -       sensed+requested  -
```

The three occupancy states are deliberately distinct:

| Occupancy | Meaning |
| --- | --- |
| `occupied` | a device is attached to the slot |
| `empty` | nothing is attached and the enclosure answered: `not installed`, or some other status |
| `unavailable` | the slot could not be read, or the enclosure reports it as `unavailable`/`unsupported` |

A slot that could not be read is never rendered as an empty one. A `-` means
"the enclosure does not expose this attribute", not zero; in JSON it is
`null`.

The FAULT column is split the way SES encodes it: the driver stores
`(status[3] & 0x60) >> 5` in the sysfs attribute, where bit 6 is FAULT SENSED
(the enclosure detected a fault) and bit 5 is RQST FAULT (somebody switched
the indicator on). So `sensed` is an alarm, `requested` is an operator's
marker, and merging them would turn one into the other. A write sets only the
requested bit, and the readback compares that bit.

Addressing an enclosure. In order of preference: the logical identifier
(`/sys/class/enclosure/*/id`, filled by the SES backend), then the unit serial
number from `sg_inq`, and only then the SCSI address. The first two survive a
reboot, the third does not, and where it is used the output marks the
identifier `temporary`. `--enclosure-id` accepts any of the three spellings;
in `capabilities` and `led` the shorter `--enclosure` means the same thing.
In `list` the short name is taken: there `--enclosure` is the output section
it has always been.

A slot is addressed by the number the enclosure reports, or by the component
name:

```sh
sudo jbod led --locate 1:0:0:0/5 --on                          # self-contained
sudo jbod led --enclosure naa.50050cc10c400000 --locate 5 --on # the same
sudo jbod led --locate "1:0:0:0/Slot 05, front" --on           # by component name
sudo jbod led --locate /dev/sda --on                           # as before
```

An empty bay can only be lit this way: it has no device path.

After a write the state is read back, with a bounded wait
(`--readback-timeout`, one second by default). A system call that returned
success is not reported as a confirmed change:

```
/dev/sda locate: on (confirmed)
1:0:0:0/5 fault: on (NOT confirmed: reads back as off)
1:0:0:0/5 locate: off (write accepted, no readback available)
```

The first line was confirmed by a read. The second means the enclosure
answered, and answered with the other state: that is a failure and a non-zero
exit code. The third means the attribute cannot be read back at all; not a
failure, but not a confirmation either. If the slot disappeared during the
operation (the drive was pulled), the command says exactly that rather than
reporting a permission problem.

`jbod capabilities` reports what a shelf can do, read and write judged
separately, with the evidence behind each verdict:

```
$ jbod capabilities
Enclosure 1:0:0:0  address naa.50050cc10c400000 (stable)  components 24
CAPABILITY         READ         WRITE        EVIDENCE
slot.enumeration   supported    unsupported  sysfs: 24 component directories
led.locate         supported    unknown      sysfs: 24/24 components expose locate, 24 readable; ...
slot.power_status  unsupported  unsupported  no component exposes power_status
disk.temperature   unknown      unsupported  scsi_temperature: installed; ...
```

The rules behind it:

- discovery writes nothing and switches nothing;
- `unsupported` for a write means the attribute is read-only, that is, the
  driver has no store handler (sysfs creates such an attribute with mode
  0444);
- `unknown` for a write means the attribute is writable, but the enclosure is
  free to accept a control page and ignore it while the kernel still returns
  success. So discovery never reports a write as `supported`; only a readback
  after a real write does;
- a transport or permission failure is shown as a separate `[error: ...]`
  field and never becomes `unsupported`.

`--json` is available on `list`, `capabilities` and `led`. An absent reading
is `null`, not zero; sections that were not requested are missing from the
document, and a requested but empty one is `[]`.

## Prometheus

Both ways of starting it run the same exporter:

```sh
./bin/jbod prometheus -i 127.0.0.1 -p 9945
./bin/prometheus-jbod-exporter 127.0.0.1 9945
```

Both binaries understand `--help` and `--version`. The version comes from
`-ldflags -X` (the Makefile passes `git describe --tags --always --dirty`),
and an unstamped build falls back to `runtime/debug.ReadBuildInfo`, so
`go install ...@v1.2.3` reports something truthful too.

The default listen address is 127.0.0.1:9945: the process can read /dev/sg*
and usually runs as root, so reaching the network should be a deliberate
choice. Binding a wildcard address (`0.0.0.0`, `::`) prints a warning.
GET / returns an empty response; GET /metrics serves Prometheus text format
0.0.4.

Tuning flags (`--help` shows the defaults):

| Flag | Default | Meaning |
| --- | --- | --- |
| `--command-timeout` | 15s | timeout of one external command |
| `--scrape-timeout` | 2m | timeout of one full collection |
| `--concurrency` | 12 | external commands allowed to run at once |
| `--cache-ttl` | 0s | serve the previous snapshot for this long |
| `--deprecated-metrics` | true | also export the pre-1.1 series (`jbod_fan_rpm`) |
| `--log-level` | info | debug, info, warn or error |
| `--log-format` | json | json for systemd, text for a terminal |

External commands run in parallel up to `--concurrency`: a 60-slot shelf
means 120 process starts, and sequentially they do not fit into a scrape
interval. The order of the output does not depend on the parallelism.

Concurrent scrapes share a single pass over the hardware, so a `curl
/metrics` next to Prometheus does not double the load on the expander.
`--cache-ttl` additionally serves a recent result without touching the
hardware at all; about half the `scrape_interval` is a reasonable value.

SIGINT and SIGTERM cancel a collection in flight immediately, without
waiting out the command timeout.

## Logging

The exporter writes structured logs to stderr (`log/slog`), JSON by default
so journald indexes the fields. It logs the start with the effective
settings, the duration of every collection, every failed command (with the
collector that lost it) and the shutdown. A clean collection is logged at
debug level and a lossy one at info, so `--log-level info` shows only what
went wrong.

```sh
journalctl -u prometheus-jbod-exporter -o cat | jq 'select(.msg=="collection error")'
```

The CLI (`list`, `led`) is quiet by default: the table itself says what is
missing (`ERR`, `N/A`, `NONE`), and failures come back as an exit code and a
line on stderr.

The metric names and label sets of the original are kept:

| Metric | Labels |
| --- | --- |
| number_of_enclosures | none |
| jbod_slot_temperature | slot, enclosure |
| jbod_fan_rpm | device, slot — **deprecated**, see below |

Added in 1.1:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| jbod_enclosure_info | gauge | enclosure, enclosure_id, id_source, vendor, model, revision, serial | the shelf's identity, always 1 |
| jbod_enclosure_slots | gauge | enclosure, enclosure_id, occupancy | slots per state |
| jbod_fan_speed_rpm | gauge | enclosure, enclosure_id, component, component_id | fan RPM without collisions |

The health of the collection itself was added:

| Metric | Type | Meaning |
| --- | --- | --- |
| jbod_up | gauge | 1 when the collection completed |
| jbod_scrape_duration_seconds | gauge | duration of the last collection |
| jbod_scrape_errors_total | counter | cumulative failures per collector (enclosures, slots, disks, fans, led) |

`jbod_enclosure_info` is the join target for every series labelled with the
SCSI address: the address is assigned at scan time and gets reassigned, so a
dashboard that needs a stable identity joins on `enclosure` and reads
`enclosure_id`. The `id_source` label says whether that is a real identifier
(`logical`, `serial`) or the address again (`address`).

The process metrics (`process_cpu_seconds_total`,
`process_resident_memory_bytes`, `process_virtual_memory_bytes`,
`process_start_time_seconds`, `process_open_fds`, `process_max_fds`) are read
from `/proc/self` on every request and never cached: the Rust version got
them from the prometheus crate, and dashboards built on them would break
without them. On a host without `/proc` (macOS, say) they are simply absent —
a missing series beats a fake zero.

### Migrating off jbod_fan_rpm

In `jbod_fan_rpm` the `device` label is the fan description and `slot` is the
sg_ses index. Both are per-shelf, so "Fan A" at index `2,0` collides with
every other shelf in the rack and the last value written wins. It cannot be
fixed in place: changing the label set would change the meaning of a series
dashboards already read.

So the corrected metric is a new name:

```
jbod_fan_speed_rpm{enclosure="1:0:0:0",enclosure_id="naa.5000...01",component="Fan A",component_id="2,0"} 1200
jbod_fan_speed_rpm{enclosure="10:0:0:0",enclosure_id="naa.5000...02",component="Fan A",component_id="2,0"} 4800
```

The migration:

1. upgrade the exporter: both series are served at once and nothing breaks;
2. move dashboards and rules to `jbod_fan_speed_rpm`;
3. run the exporter with `--deprecated-metrics=false` and check nothing went
   missing;
4. leave it there. Removing `jbod_fan_rpm` is planned for 2.0 at the
   earliest.

Every scrape collects fresh values. Hardware that disappeared drops out of
the output. An unavailable temperature is skipped (the CLI shows ERR); an
unavailable firmware revision shows as N/A.

A partial collection is an HTTP 200: a broken sensor, an unreadable sysfs
tree for one enclosure or a fan without an RPM reading are counted in
`jbod_scrape_errors_total` and everything else is served as usual. HTTP 503
is left for a total failure — the enclosure listing itself did not work (no
lsscsi, no driver). The CLI still treats such failures as fatal and prints no
half table; the exception is a fan without an RPM reading, which is skipped.

## Differences from the Rust version

- The exporter runs in the foreground; SIGINT and SIGTERM shut the HTTP
  server down cleanly. Running in the background is systemd's job — no fork
  and no second child process.
- Tables are plain text, with no colours and no blinking ANSI sequences.
- Tools are resolved to absolute paths against a fixed PATH and run with a
  clean environment; help does not need the SCSI utilities installed.
- Options are parsed by pflag, so clap compatibility is literal rather than
  approximate.
- Failures exit non-zero instead of panicking or succeeding silently.
- VPD page 0x80 is parsed as binary, with its header and length.
- Slots and disks are separate: every bay of the enclosure is listed, empty
  ones included, with its number, type, status, power state and indicators.
  Non-standard component names are supported.
- LED control does not depend on scsi_temperature or sginfo being available;
  the result of a write is read back, and an unconfirmed write is called one.
- There is a `capabilities` command, and `--json` on `list`, `capabilities`
  and `led`.
- Exactly one of --on and --off is required. An unknown device is an error.
- Metric compatibility is complete, `process_*` included; on top of the Rust
  version there are `jbod_up`, `jbod_scrape_duration_seconds`,
  `jbod_scrape_errors_total`, `jbod_enclosure_info`, `jbod_enclosure_slots`
  and `jbod_fan_speed_rpm`.

## Installing, and the Debian package

```sh
sudo make install
sudo systemctl daemon-reload
sudo systemctl enable --now prometheus-jbod-exporter
```

`make install` puts both binaries in /usr/bin, the unit in
/lib/systemd/system and the argument file in
/etc/default/prometheus-jbod-exporter. Set DESTDIR to install into a staging
directory. If you change PREFIX, adjust ExecStart in the unit.

Arguments are set in `/etc/default/prometheus-jbod-exporter` rather than by
editing the unit (which reads it through `EnvironmentFile=-` and expands
`$ARGS`). The exporter listens on 127.0.0.1 only, so for scraping from
another host:

```sh
echo 'ARGS="--ip-address 10.0.0.7 --port 9945"' > /etc/default/prometheus-jbod-exporter
systemctl restart prometheus-jbod-exporter
```

The unit deliberately does not set `PrivateDevices=`: it would hide the
`/dev/sg*` devices the exporter exists to read. The rest of the hardening is
in place: `ProtectSystem=strict`, `NoNewPrivileges`, `ProtectHome`,
`PrivateTmp`, `ProtectKernel*`, `RestrictNamespaces`,
`MemoryDenyWriteExecute`, `SystemCallFilter=@system-service`,
`CapabilityBoundingSet=CAP_SYS_RAWIO CAP_DAC_OVERRIDE`.

When the preflight check fails (missing tools, unreadable
/sys/class/enclosure) the exporter refuses to start and logs the reason. With
`Restart=on-failure` that means a restart loop until it is fixed, but the
reason is visible immediately.

Building needs access to the module (`go mod download`) or a `vendor/`
directory (`make vendor`): the single pflag dependency is not committed.

On a Linux host with dpkg, `make deb` builds the package. The result is
`dist/jbod-go_<version>_<arch>.deb` plus `dist/SHA256SUMS`. The package
version comes from `git describe`; with no tags yet describe returns a bare
commit hash, which becomes `0.0.0+<hash>` because a Debian version has to
start with a digit. The maintainer comes from `git config
user.name/user.email`, and a host without a git identity (a CI runner,
typically) has to pass one: `make deb MAINTAINER="Name <address>"`. The
package carries conffiles, md5sums and postinst/prerm/postrm using
`deb-systemd-helper`. Building the Debian package on macOS is untested (no
dpkg-deb).

Releases are built by goreleaser from a `v*` tag (`.goreleaser.yaml`,
`.github/workflows/release.yml`): archives for linux/amd64 and linux/arm64,
a `.deb` through nfpm, and `SHA256SUMS`.

## Checks

```sh
make test      # go vet + go test -race
make lint      # gofmt gate + golangci-lint when installed
make cover     # coverage with -covermode=atomic
GOOS=linux GOARCH=amd64 go build ./...
GOOS=linux GOARCH=arm64 go build ./...
```

The tests use a temporary sysfs tree and stubbed SCSI tool output:
discovery, VPD, temperature, RPM, LED control, metric compatibility,
disappearing devices, HTTP status codes and the CLI. Separately covered:
parallel collection and the concurrency limit, overlapping scrapes sharing
one pass, the TTL cache, partial collection with its health metrics, and a
collection in flight being cancelled when the daemon stops. The parsers of
tool output have table tests and fuzz targets:

```sh
go test -run xxx -fuzz FuzzParseVPD80 -fuzztime 30s ./internal/jbod/
```

The `list` output and the metric text are pinned by golden files, so an
extra or missing line shows up as a diff instead of slipping through:

```sh
go test ./internal/cli/ ./internal/metrics/ -update   # rewrite the golden files
```

CI (`.github/workflows/go.yml`) runs the tests on Go 1.25 and 1.26, the
`gofmt` gate, `go vet`, golangci-lint, govulncheck, a short fuzzing pass over
the parsers, builds for linux/amd64 and linux/arm64, and a `.deb` build with
`dpkg-deb --contents`.

None of this was verified against a real JBOD. Before relying on it, check
the `list` output and the metrics against your own hardware: stubbed
responses are no substitute for different sg3-utils versions and enclosure
models.

Layout:
- cmd/jbod, cmd/prometheus-jbod-exporter — entry points, one line each: the
  shared body of both binaries is `internal/cli.Main`.
- internal/jbod — domain types, collection through sysfs and the sg3-utils,
  LED control; `parse.go` holds the pure parsers of tool output (table tests
  and fuzzing), `order.go` the natural slot ordering, `exec.go` the tool
  resolution and the preflight check.
- internal/metrics — encoding a snapshot as Prometheus text format 0.0.4.
- internal/exporter — the HTTP handler, the scrape timeout, sharing
  concurrent scrapes and the TTL cache.
- internal/process — the `process_*` metrics from `/proc/self`.
- internal/cli — argument parsing: `list.go`, `led.go`, `prometheus.go`,
  table rendering in `output.go`, the version in `version.go`, the logger in
  `log.go`.
