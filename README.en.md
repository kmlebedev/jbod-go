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
sudo ./bin/jbod led --locate /dev/sda --on
sudo ./bin/jbod led --locate /dev/sda --off
sudo ./bin/jbod led --fault /dev/sg1 --on
./bin/jbod prometheus --ip-address 127.0.0.1 --port 9945
```

`-e`, `-d` and `-f` are independent: each adds its own section, so `list -ef`
prints both the enclosures and the fans. Disks are ordered naturally —
`Slot 2` comes before `Slot 10`.

Option parsing is POSIX: short flags group (`-ed`, `-edf`), long flags take
`--flag=value`, `--` ends the options, and every subcommand has `--help`.
For the exporter `--ip` and `--ip-address` are one option under two
spellings, not two flags where the last one silently wins.

LEDs can be given more than once: `led -l /dev/sda -l /dev/sdb --on`. Both
/dev/sg* paths and the matching /dev/sd* names work. Writes happen in order
and stop at the first failure; the ones that already succeeded are not rolled
back.

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
| jbod_fan_rpm | device, slot |

The health of the collection itself was added:

| Metric | Type | Meaning |
| --- | --- | --- |
| jbod_up | gauge | 1 when the collection completed |
| jbod_scrape_duration_seconds | gauge | duration of the last collection |
| jbod_scrape_errors_total | counter | cumulative failures per collector (enclosures, disks, fans) |

The process metrics (`process_cpu_seconds_total`,
`process_resident_memory_bytes`, `process_virtual_memory_bytes`,
`process_start_time_seconds`, `process_open_fds`, `process_max_fds`) are read
from `/proc/self` on every request and never cached: the Rust version got
them from the prometheus crate, and dashboards built on them would break
without them. On a host without `/proc` (macOS, say) they are simply absent —
a missing series beats a fake zero.

As in the original, a fan's `device` label is the fan description and `slot`
is the sg_ses index. Enclosures with identical descriptions and indices
collide on those labels, and the last value wins. That limitation of the
original metric schema is kept for compatibility.

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
- Slots are found by the presence of device/scsi_generic, including
  non-standard slot names.
- LED control does not depend on scsi_temperature or sginfo being available.
- Exactly one of --on and --off is required. An unknown device is an error.
- Metric compatibility is complete, `process_*` included; on top of the Rust
  version there are `jbod_up`, `jbod_scrape_duration_seconds` and
  `jbod_scrape_errors_total`.

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
