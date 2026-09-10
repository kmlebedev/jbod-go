# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/1.1.0/) and
the project uses [semantic versioning](https://semver.org/spec/v2.0.0.html).
Versions are git tags (`v1.2.3`); the binaries report `git describe` when
built from a checkout.

## Unreleased

Post-port review of the Go port of [Gandi/jbod-rs](https://github.com/Gandi/jbod-rs).
The CLI output and the original metric names are unchanged throughout.

### Added

- `jbod_up`, `jbod_scrape_duration_seconds` and
  `jbod_scrape_errors_total{collector}`: a partial collection is now an HTTP
  200 with these series instead of a 503.
- The `process_*` metrics (`process_cpu_seconds_total`,
  `process_resident_memory_bytes`, `process_virtual_memory_bytes`,
  `process_start_time_seconds`, `process_open_fds`, `process_max_fds`), read
  from `/proc/self`, restoring compatibility with dashboards built on the
  Rust exporter.
- Structured logging (`log/slog`): JSON by default for journald, `--log-level`
  and `--log-format` to change it. Every failed command is logged once with
  its collector, next to the start parameters, the duration of each
  collection and the shutdown.
- A preflight check that reports every missing tool, with its package, and an
  unreadable or empty `/sys/class/enclosure`, before any work starts.
- Exporter tuning flags: `--command-timeout`, `--scrape-timeout`,
  `--concurrency`, `--cache-ttl`.
- Arguments for the packaged service come from
  `/etc/default/prometheus-jbod-exporter`; the systemd unit is hardened
  (`ProtectSystem=strict`, `SystemCallFilter=@system-service`, a restricted
  capability set, and deliberately no `PrivateDevices`).
- The Debian package carries conffiles, md5sums, a changelog and
  postinst/prerm/postrm using `deb-systemd-helper`; goreleaser builds
  archives, a `.deb` and `SHA256SUMS` from a `v*` tag.

### Changed

- Collection runs in parallel with a bounded worker pool (`--concurrency`,
  12 by default). A 60-slot shelf used to mean 120 sequential process starts,
  which does not fit into a scrape interval.
- Overlapping scrapes share one pass over the hardware; `--cache-ttl` can
  additionally serve a recent result without touching it.
- The exporter listens on `127.0.0.1` by default (it can read `/dev/sg*` and
  usually runs as root) and warns when bound to a wildcard address. **The
  packaged service therefore becomes loopback-only: set `ARGS` in
  `/etc/default/prometheus-jbod-exporter` to scrape it from another host.**
- Options are parsed with `spf13/pflag`, the first dependency: short flags
  group (`-ed`), long flags take `--flag=value`, `--` ends the options, and
  `--ip`/`--ip-address` are one option instead of two registrations where the
  last one silently wins.
- Missing readings are absent values rather than the `ERR`/`N/A`/`NONE`
  sentinel strings, which now exist only in the CLI output layer.
- `SetLED` is a client method taking a typed `LEDKind`, and resolves the
  sysfs attribute itself; the disk listing no longer carries file paths.
- The package layout is split: collection (`internal/jbod`, with the pure
  parsers in `parse.go`), encoding (`internal/metrics`), serving
  (`internal/exporter`), the `process_*` metrics (`internal/process`) and the
  CLI with one file per command.
- The version is stamped through `-ldflags -X` with `runtime/debug` as a
  fallback, instead of a constant kept in sync by hand.
- Go 1.25 is the minimum; CI runs 1.25 and 1.26, with a gofmt gate,
  golangci-lint, govulncheck, cross-builds and a `.deb` build.

### Fixed

- `jbod list -e -f` silently dropped the fan section.
- Disks were ordered lexicographically, so `Slot 10` came before `Slot 2`.
- One broken SES index or one unreadable enclosure sysfs tree failed the
  whole scrape, taking the temperatures and the enclosure count with it.
- `prometheus-jbod-exporter --version` failed to parse its own flag.
- A scrape in flight did not see SIGTERM until the connection closed, so
  shutdown waited out the command timeout.
- The enclosure device is taken from the generic-device column of
  `lsscsi -g` and required to be a `/dev/sg*` path, instead of the first
  `/dev/` token on the line.
- External tools are resolved once to absolute paths against a fixed PATH and
  run with `LC_ALL=C`, so a root daemon does not depend on its inherited
  environment and the parsers see English output.
- `SetLED` no longer compares a device against the `NONE` sentinel, which
  could never match.
