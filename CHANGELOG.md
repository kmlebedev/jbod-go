# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/1.1.0/) and
the project uses [semantic versioning](https://semver.org/spec/v2.0.0.html).
Versions are git tags (`v1.2.3`); the binaries report `git describe` when
built from a checkout.

## Unreleased

### Changed

- The one-line output of `smp_discover --multiple` is parsed in layers —
  phy number, then the routing letter if there is one, then the attached
  device if there is one, then whatever words the expander used instead.
  It prints three shapes, not one: "inaccessible (phy vacant)",
  "<letter>:disabled" and the attached form, and only the third was
  recognised. A phy the expander reports as vacant is now a state of its
  own, and its error log is not asked for: the expander already answered,
  and on one real expander that is 24 requests per pass that would each
  return an error whose reason is known.
- After the second run on real hardware, this time with smp_utils
  installed, four more things were corrected. The phy list of an expander is
  built from the count the expander reports rather than from what
  `smp_discover --multiple` happened to describe — it described 24 of one
  expander's 49 phys, and the other 25 were never asked for their error log.
  The negotiated rate is cut at the first field boundary, because this
  expander appends the zone group after it ("12 Gbps  ZG:14"). The routing
  attribute is passed through as the letter smp_discover prints, instead of
  expanding three letters and leaving the fourth raw. And the parent device
  of a phy is read past the class directory: sysfs nests a class entry as
  <device>/sas_phy/<name>, so every phy on a real machine reported "sas_phy"
  as its parent.
- The evidence behind `sas.phy_error_counters` separates a counter that
  exists from one that answers. On a populated shelf 199 of 391 phys answer
  and the rest fail per phy, which is the driver asking the expander, not a
  driver without counters.
- After the first run on real hardware (a WD H4060-J behind one HBA: 391
  phys, six expanders, no smp_utils installed), four things in the phy
  report were corrected. A counter that is absent now says which of the two
  things happened — the driver publishes none, or the attribute exists and
  the read failed, which is what an expander phy with nothing attached does.
  The SMP note groups identical reasons instead of repeating one sentence
  once per expander. The "not found" message reads the same whether the tool
  lookup was cached or not, and names the package to install. The note about
  the visibility boundary no longer says "the named shelf" when no shelf was
  named.
- The expanders of a host are rendered as a table, and a per-expander phy
  table appears only for an expander that answered over SMP. Six expanders
  used to take six one-line stanzas with a blank line before each.
- The exposition is now built with
  [prometheus/client_golang](https://github.com/prometheus/client_golang)
  v1.24.1 instead of a hand-written text encoder. `internal/metrics` is a
  `prometheus.Collector` that hands const metrics to the library, and
  `internal/exporter` serves them through `promhttp` over a registry. Every
  series keeps its name, its labels and its meaning; what changes in the
  output is what the library owns: families are sorted by name, labels are
  sorted within a series, a family with no samples is no longer announced
  with a bare `# HELP`, and floats are formatted by the library (the
  snapshot timestamp now reads `1.7899848e+09`).
- The response goes through content negotiation, so a scrape that asks for
  OpenMetrics or for gzip gets it. Prometheus' default text format 0.0.4 is
  unchanged.
- The scrape budget, the sharing of concurrent scrapes and the TTL cache are
  unchanged and still live in `internal/exporter`: they hold a collected
  snapshot, which is what a `prometheus.Collector` cannot do, because a
  hardware pass needs a context and a deadline.

### Added

- `jbod phy [ENCLOSURE] [--smp] [--json]`: the SAS links behind a shelf.
  For every phy of the host it reports the SAS address, the negotiated,
  minimum and maximum link rate, the state and the four SAS link error
  counters, read from `/sys/class/sas_phy` without running a single
  external command. Expanders are listed from `/sys/class/sas_expander`
  with their identity and the bsg node SMP can be addressed to
  (ROADMAP 6, first two items).
- `--smp` additionally asks every expander over SMP: `smp_rep_general` for
  the phy count, `smp_discover --multiple` for the address at the far end of
  each link, and one `smp_rep_phy_err_log` per phy for its error counters.
  It is opt-in because that is one request per phy through one SMP
  processor. `smp_utils` is needed only by this flag, so it is not a
  dependency and the preflight check does not look for it.
  The `--zero` option, which clears the counters it reports, is never
  passed.
- Capabilities `sas.phy`, `sas.phy_error_counters` and
  `smp.phy_error_counters`, with the evidence behind each verdict. None of
  them ever reports write support: a link rate and a state are reports of
  what the link did, not settings.
- Metrics `jbod_sas_phy_info`, `jbod_sas_phy_up`,
  `jbod_sas_phy_negotiated_link_rate_gbps` and the counters
  `jbod_sas_phy_invalid_dword_total`,
  `jbod_sas_phy_running_disparity_error_total`,
  `jbod_sas_phy_loss_of_dword_sync_total` and
  `jbod_sas_phy_reset_problem_total`. They carry the hardware's own running
  total as a Prometheus counter, so a hardware reset reads as a counter
  reset and never as a negative increase; accumulating them in the exporter
  would be wrong across a restart. The labels are the host and the phy, not
  the enclosure, because a phy belongs to the HBA.
- A `sas` collector in `jbod_scrape_errors_total`, and the phys in the
  snapshot the exporter publishes. The scrape reads sysfs only.
- `jbod_build_info` with the version and the toolchain of the running
  binary, and `promhttp_metric_handler_requests_total` with the /metrics
  responses by status code.
- `go_*` runtime metrics (goroutines, GC, runtime memory) from the client
  library's Go collector.

### Removed

- `internal/process`, the hand-written `/proc/self` reader. The `process_*`
  series are now published by client_golang's process collector, which is
  where they came from in the Rust original (A9). The series and their
  meanings are the same.

### 1.2 — enclosure health and sensors

The enclosure stops being a list of bays: every SES element it declares is
read, with its condition, its sensors and the thresholds it publishes for
them. What the hardware reports and how much of it could be read are two
separate answers throughout. ROADMAP section 5.

#### Added

- `jbod health` reports the condition of a shelf in three rows that answer
  three different questions: `hardware` is the enclosure's own verdict from
  the five bits of the Enclosure Status page, `components` is the worst
  condition among its elements, and `collection` is how complete the poll
  was. A page that did not answer lands in the collection row and in the
  notes, never in the hardware verdict, and a condition bit that was not
  read prints as `-` rather than as `0`.
- `jbod sensors` prints the temperature, voltage, current and speed values
  the enclosure reports, each with the thresholds it declares for that
  element on the Threshold In page. The page is read and never written.
- `jbod list --components` (`-c`) lists every element the enclosure
  declares, including the ones no status page reported, which are marked
  `declared only` instead of being dropped.
- The slot → SAS address → disk mapping: the address comes from the
  Additional Element Status page, the disk from the sysfs slot walk, and the
  bay number the enclosure reports ties them together, falling back to the
  element number and then to the component name. The null SAS address is
  never published as an identity.
- Six health levels — `ok`, `warning`, `critical`, `unrecoverable`, `absent`
  and `unknown` — with `absent` and `unknown` counted but kept out of the
  worst-of, so thirty bays owned by the other I/O module of a chassis do not
  make a working shelf report `unknown`, and an element nobody could read
  never reports `ok`.
- Every reading carries its value, unit, source, read time and the reason a
  value is absent. A sensor that declares a reading and reported none keeps
  its row with the value absent, because a dropped row is indistinguishable
  from a sensor that never existed.
- New metrics: `jbod_enclosure_health{source,level}`,
  `jbod_enclosure_components{type,health}`, `jbod_component_info`,
  `jbod_sensor_temperature_celsius`, `jbod_sensor_voltage_volts`,
  `jbod_sensor_current_amps`, their `_threshold_` counterparts and
  `jbod_slot_sas_address_info`. Health is a label and never a number,
  because a numeric severity has nowhere right to put `unknown`, and a
  reading the hardware did not report gets no series at all.
- Collection metrics: `jbod_snapshot_timestamp_seconds`,
  `jbod_collection_complete`, `jbod_ses_page_read{page,required}`,
  `jbod_enclosure_generation_changed` and
  `jbod_enclosure_components_missing`, plus the new `components` collector in
  `jbod_scrape_errors_total`. An enclosure in trouble and an enclosure
  nobody could read are different alerts and now have different series.
- `--json` on `health`, `sensors` and `list --components`, with the same
  rules as the existing documents: an absent reading is `null`, a section
  nobody asked for is missing, and a requested empty one is `[]`.

#### Changed

- One collection pass now also reads the SES pages, so the exporter and the
  command line render the same snapshot rather than collecting twice. The
  slot walk is shared with the inspection instead of being repeated.
- `Snapshot` carries `Status` (the per-shelf reports) and `ReadAt` (when the
  pass started), which is what the freshness metric publishes.
- The shelf selector helper is shared by `list`, `capabilities`, `health`
  and `sensors`: the shelf is either the value of `--enclosure-id` or the
  single operand, and naming it twice is an error in all four.

#### Notes

- The four pages are read per shelf and in sequence — `--page=cf`,
  `--join`, `--page=es` and, only when the shelf reports sensor elements,
  `--page=th` — while different shelves are read in parallel. The join is
  the one call that carries the descriptors, the statuses and the
  Additional Element Status together, which is what makes the mapping
  possible without a page read per element.
- The generation code is recorded from every page. When the pages disagree
  they describe different configurations, so the report is marked as a
  mixture and the collection is not complete; the pass is not repeated,
  because a shelf being reconfigured would repeat forever.
- The Threshold In page is optional: a shelf that does not implement it is
  not a shelf that failed to answer, and the collection stays complete.
- The element type is accepted in the three shapes sg_ses has printed it in
  (`[3,0]  Element type: Cooling`, `Fan A [2,0]  Cooling element`, and a
  standalone type header above its elements), because a parser that knows
  only one of them reports a shelf with no cooling elements on the versions
  that print another.
- `jbod_fan_speed_rpm` is unchanged and still comes from the fan collector.
  The cooling elements also appear as components, with their speed as a
  reading; the two agree because they read the same enclosure, and the fan
  metric stays the one to alert on.
- None of this has been verified against hardware yet. The parsers are
  covered by fixtures for the page shapes above; the matrix in ROADMAP 10
  still applies.

### 1.1 — correct inventory and capabilities

Slots and disks become separate things, enclosures get an identity that
survives a reboot, LED writes are confirmed rather than assumed, and the
colliding fan metric is replaced. ROADMAP section 4.

#### Added

- `jbod list --slots` lists every bay of an enclosure, empty ones included,
  with the slot number, type, status, power state and both indicators. The
  walk starts from the enclosure's components instead of
  `device/scsi_generic`, so an empty bay exists in the model for the first
  time. Occupancy has three states — `occupied`, `empty` and `unavailable` —
  and a slot that could not be read is never rendered as an empty one.
- `jbod capabilities` reports what a shelf can do, read and write judged
  separately, with the evidence behind each verdict and `supported` /
  `unsupported` / `unknown` as the three answers. Discovery performs no
  writes, and it never reports a write as supported: an enclosure may accept
  a control page and ignore it while the kernel returns success, so only a
  readback after a real write can confirm one. A transport or permission
  failure is reported apart from the verdict, never as `unsupported`.
- The fault indication is split into the fault the enclosure detected and
  the fault somebody requested, following the SES encoding the driver packs
  into the sysfs attribute (`(status[3] & 0x60) >> 5`).
- `Enclosure.ID` from the sysfs `id` attribute — the enclosure logical
  identifier — with a documented fallback to the unit serial number and then
  to the SCSI address, which is marked `temporary` wherever it is used.
  `--enclosure-id` (also `--enclosure` where the name is free) accepts any of
  the three spellings.
- LED targets can now be slots: `led --locate 1:0:0:0/5`, `led --enclosure
  <id> --locate 5`, or the component name. An empty bay can only be lit this
  way, because it has no device path. The existing `/dev/sg*` and `/dev/sd*`
  syntax is unchanged.
- Every LED write is read back within `--readback-timeout` (one second by
  default). A write the kernel accepted is reported as `confirmed` only when
  the enclosure reports the requested state; a readback that shows the other
  state is a failure, and an attribute that cannot be read back is reported
  as such rather than as a success.
- A slot that disappears during an operation is reported as exactly that
  (`ErrSlotGone`), separately from a permission or I/O failure.
- `--json` on `list`, `capabilities` and `led`. An absent reading is `null`,
  never a zero; a section that was not requested is missing from the
  document, a requested and empty one is `[]`.
- `jbod_fan_speed_rpm{enclosure,enclosure_id,component,component_id}`, the
  corrected fan metric.
- `jbod_enclosure_info{enclosure,enclosure_id,id_source,vendor,model,revision,serial}`,
  the join target for every series labelled with the SCSI address, and the
  marker that says when that identity is only the address again.
- `jbod_enclosure_slots{enclosure,enclosure_id,occupancy}`, the slot counts
  per state, published for all three states so a count reaching zero stays
  visible.
- `--deprecated-metrics=false` for the exporter, to drop the pre-1.1 series
  once nothing reads them.

#### Changed

- Disks are now a projection of the slot walk, so the disk view and the slot
  view cannot disagree. `Disk` carries `EnclosureID` and `SlotNumber`.
- `jbod_scrape_errors_total` gained the `slots` and `led` collectors. An
  unreadable enclosure tree is now counted against `slots`, which is what
  enumerates the bays; `disks` counts the per-disk telemetry only.
- `Client.SetLED` takes a `LEDTarget` and returns an `LEDResult`
  (internal API).

#### Fixed on hardware

First run against a physical shelf — a WD/HGST H4060-J, 60 bays, two I/O
modules, 60 disks — found three defects the fixtures could not produce, and
a fourth weakness worth fixing while there. All of it was re-run on the same
shelf afterwards: 30 occupied and 30 unavailable bays per module with no
false empties, temperatures reading 31-32 °C where all sixty had read ERR,
and eight cooling elements with no overall element among them.

- Thirty populated bays per module were listed as `empty`. Each I/O module
  registers its own sysfs enclosure listing all sixty bays and reports the
  thirty it does not own with a status the driver has no name for:
  `enclosure.c` indexes its name table with the raw SES element status, and
  code 8, "no access allowed", is past the end of it, so sysfs prints a
  literal `(null)`. The occupancy rule is now strict — a bay is `empty` only
  when the enclosure says `not installed`, and everything else unexplained
  is `unavailable` with the reason attached.
- Every one of the sixty disks reported `Temp: ERR`. `scsi_temperature`
  wraps `sg_logs --temperature`, which prints `Current temperature = 33 C`
  with an equals sign, and the parser required a colon. Both separators are
  accepted now; a line with neither is still rejected, because guessing a
  number out of "Current temperature sensor 2 unavailable" is worse than
  reporting nothing.
- The SES overall element of the cooling type was listed as a fan. The shelf
  reports it as `[3,-1] ... Fan stopped` at 0 RPM, which put a dead fan in
  front of an operator whose fans were all running and a zero-RPM series in
  front of an alert rule. Overall elements are no longer listed as devices.
- A cooling element whose speed cannot be read was dropped from the
  listing entirely. This shelf did not demonstrate it — all eight of its
  fans answer — but a dropped row is indistinguishable from a fan that was
  never there, which is the same mistake as reporting a missing reading as
  zero. `Fan.Speed` is now optional: the element stays, with a dash instead
  of a speed, and gets no metric series.

Also from that run:

- Narrowing a listing to one shelf could not be discovered. `--enclosure-id`
  reads like a section of its own, so `jbod list --enclosure-id <id>`
  answered "list requires --enclosure", and pasting the identifier after
  `-e` answered "list takes no arguments" — five attempts on the shelf, none
  of which worked. The shelf is now the one positional argument of `list`
  and `capabilities` (`jbod list -e 0x5000...`), naming a shelf without a
  section implies `--enclosure`, and the generic device joins the SCSI
  address, the logical identifier and the serial number as a way to name
  one. The device is also the only one of the four that tells two I/O
  modules of a chassis apart.
- An enclosure identifier can match two sysfs enclosures, because it names
  the chassis and not the I/O module. `list --slots` and `capabilities` say
  so in the heading, and `led` picks the path that owns the bay — a write
  through the module with no access is accepted and lights nothing. The LED
  result line now names the enclosure and slot it resolved to.
- `list --enclosure` printed the table header once per shelf.
- `--fans`, `--enclosures`, `--disk` and `--slot` are accepted as spellings
  of the existing flags.

#### Deprecated

- `jbod_fan_rpm`. Its labels are the fan description and the sg_ses index,
  both of which are per-shelf, so identical fans on two enclosures overwrite
  each other. Relabelling it in place would change the meaning of a series
  dashboards already read, so the fix is the new name above and the old
  series stays, exported by default, until 2.0. `--deprecated-metrics=false`
  turns it off once the migration is done; README has the steps.

### Post-port review

Post-port review of the Go port of [Gandi/jbod-rs](https://github.com/Gandi/jbod-rs).
The CLI output and the original metric names are unchanged throughout.

#### Added

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

#### Changed

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

#### Fixed

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
