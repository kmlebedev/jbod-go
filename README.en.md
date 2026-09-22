# jbod-go

A Go port of [Gandi/jbod-rs](https://github.com/Gandi/jbod-rs).
Ported from revision `54fb20260aa0d5c88855fb71f3b9b7faf2d21e13`.
BSD-2-Clause; the original notices are kept in LICENSE.

Russian version: [README.md](README.md).

## Requirements and building

Go 1.25+ and two dependencies:
[prometheus/client_golang](https://github.com/prometheus/client_golang), the
official Prometheus client the exporter publishes through, and
[spf13/pflag](https://github.com/spf13/pflag) for POSIX option parsing.
Talking to hardware needs Linux, the enclosure
driver, a readable /sys/class/enclosure and the lsscsi, sg_inq, sg_map,
sg_ses, sginfo and scsi_temperature tools. On Debian and Ubuntu install the
lsscsi and sg3-utils packages. Reading the SAS links (`jbod phy`) needs
nothing beyond /sys/class/sas_phy; its SMP half (`jbod phy --smp`) needs
smp_utils and is the only thing that does, so that package is not a
dependency and the preflight check does not look for it.
Reading /dev/sg* and writing LEDs need the
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

`-e`, `-d`, `-f`, `-s` and `-c` are independent: each adds its own section, so
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
| `empty` | nothing is attached and the enclosure explicitly says `not installed` |
| `unavailable` | everything else: the slot could not be read, or nothing is attached and the enclosure did not say the bay is empty |

The rule is deliberately strict. A chassis with two I/O modules (verified on
a WD H4060-J) registers one sysfs enclosure per module, each listing all 60
bays, and reports the 30 it does not own with a status the driver cannot
name: `enclosure.c` indexes its name table with the raw SES status, and code
8, "no access allowed", is past the end of that table, so sysfs prints a
literal `(null)`. Reading "nothing attached and no explanation" as an empty
bay turns 30 populated slots into 30 empty ones — the exact confusion the
three states exist to prevent. The reason is printed once as a footnote
under the table rather than as a column on every row.

A slot that could not be read is never rendered as an empty one. A `-` means
"the enclosure does not expose this attribute", not zero; in JSON it is
`null`.

The FAULT column is split the way SES encodes it: the driver stores
`(status[3] & 0x60) >> 5` in the sysfs attribute, where bit 6 is FAULT SENSED
(the enclosure detected a fault) and bit 5 is RQST FAULT (somebody switched
the indicator on). So `sensed` is an alarm, `requested` is an operator's
marker, and merging them would turn one into the other. A write sets only the
requested bit, and the readback compares that bit.

A shelf is selected by any of the four spellings the tables themselves
print — the logical identifier, the serial number, the SCSI address or the
generic device:

```sh
jbod list -e 0x5000ccab05629d00       # positionally
jbod list -e --enclosure-id /dev/sg2  # the same, as a flag
jbod list --slots 1:0:31:0            # this path only
jbod list 0x5000ccab05629d00          # no section given: lists the enclosures
jbod capabilities /dev/sg33
```

The shelf is the only positional argument `list` and `capabilities` take, so
it needs no flag. Naming a shelf and no section implies `--enclosure`.

One identifier can belong to two sysfs enclosures: it names the chassis,
not the module. Of the four spellings only the generic device (`/dev/sg2`
against `/dev/sg33`) tells the modules apart — they share the identifier and
the serial number. When it does, the table heading says so (`same chassis as
1:0:31:0`), and `led` picks the path that owns the bay — through the other
module the write would be accepted and light nothing.

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

An empty bay can only be lit this way: it has no device path. The result
line says where the write actually went (`[1:0:31:0 slot 30, /dev/sg34]`).

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

## Enclosure health, components and sensors

`jbod health` answers "what is wrong with this shelf", `jbod sensors` shows
the numbers that answer rests on, and `jbod list --components` lists every
SES element the enclosure declares: bays, power supplies, fans, sensors and
I/O modules.

One pass reads four pages: Configuration (`--page=cf`), Enclosure Status
(`--page=es`), the join of Enclosure Status, Element Descriptor and
Additional Element Status (`--join`), and Threshold In (`--page=th`).
Nothing is written: reading a threshold is a read, and changing thresholds
or cooling is 1.4.

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

The three rows answer three different questions and are deliberately not
collapsed into one:

| Row | What it is |
| --- | --- |
| `hardware` | the enclosure's own verdict: the five bits of the Enclosure Status page |
| `components` | the worst condition among its elements |
| `collection` | how complete the poll was: which pages answered, and whether they agreed on the generation code |

A fault and a failed poll are different events. A shelf whose page did not
answer does not become healthy, and a shelf that does not implement the
threshold page does not become broken. So a page that failed lands in the
`collection` row and in the notes, never in `hardware`, and a condition bit
that was not read prints as `-` rather than as `0`.

The levels:

| Level | When |
| --- | --- |
| `ok` | the enclosure reports `OK` |
| `warning` | SES `noncritical` |
| `critical` | SES `critical` |
| `unrecoverable` | SES `unrecoverable` |
| `absent` | `not installed`: the element is declared and is not there |
| `unknown` | `unsupported`, `unknown`, `not available`, `no access allowed`, or nothing was read |

`unknown` and `absent` are counted but take no part in the worst-of. A
two-module chassis would otherwise always report `unknown`: thirty bays of
the far module answer with "no access allowed", which is the boundary of
what that module can see and not a diagnosis of the shelf. When nothing at
all was readable the answer is `unknown`, because then there is no
observation to report.

An element the Configuration page declares and no status page reported stays
in the listing as `declared only`. "The shelf says it has two power supplies
and one of them answered" is a finding, and a table showing one power supply
hides it.

```
$ jbod sensors
Enclosure 1:0:0:0  address 0x5000ccab05629d00 (stable)
ID   NAME        TYPE                READING      VALUE  UNIT     STATUS       HEALTH   HIGH CRIT  HIGH WARN  LOW WARN  LOW CRIT
1,0  PSU A       power supply        temperature  41     celsius  OK           ok       -          -          -         -
3,0  TEMP IOM A  temperature sensor  temperature  35     celsius  OK           ok       65         60         0         -19
3,1  TEMP IOM B  temperature sensor  temperature  -      celsius  Unsupported  unknown  -          -          -         -
```

The thresholds are the enclosure's own numbers from the Threshold In page,
not a constant in an alerting rule: the next shelf declares different ones.
A sensor that declares a reading and reported no value keeps its row with a
`-`: a dropped row is indistinguishable from a sensor that never existed,
and a zero is a lie. In JSON every reading carries its value, unit, source,
read time and the reason the value is absent.

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

The SAS ADDRESS, DEVICE and MAP columns are the slot → SAS address → disk
mapping: the address comes from the Additional Element Status page, the disk
from the sysfs walk, and what ties them together is the bay number the
enclosure itself reports (falling back to the element number and then to the
component name). The null address is never published: it is not an identity,
and every empty bay would join on it.

The generation code is read from every page. When they disagree, the pages
describe different configurations: the report is marked as a mixture and
`collection` becomes `partial`. The pass is not repeated — a shelf being
reconfigured would repeat forever — and what happened is said plainly
instead.

`--json` is available on `health`, `sensors` and `list --components`. The
commands decide nothing for the operator: `jbod health` prints the condition
and exits 0 unless the collection itself failed.

## SAS connections and error counters

`jbod phy` reports the links a shelf is attached by rather than the shelf:
the SAS address of every phy, the rate the link came up at, its state and
the error counters the hardware itself keeps. It answers the situation
`health` and `sensors` cannot show: every element of the enclosure reads
`OK` and the disks keep timing out. That is a cable, and this is the only
place it is visible.

```
$ jbod phy 0x5000ccab05629d00
Host 1  3 phys: 2 up, 1 disabled  (enclosures 1:0:0:0)
PHY        PORT      TYPE           SAS ADDRESS         ID  STATE     NEGOTIATED    MAX        INV DW  DISP  SYNC  RESET
phy-1:0    port-1:0  end device     0x500605b00b1e2f40  0   up        12.0 Gbit     12.0 Gbit  0       0     0     0
phy-1:1    port-1:0  end device     0x500605b00b1e2f41  1   up        6.0 Gbit      12.0 Gbit  1274    7     31    2
phy-1:0:0  -         edge expander  0x5000ccab05629d3f  0   disabled  Phy disabled  -          -       -     -     -

Expander expander-1:0  address 0x5000ccab05629d3f  HGST H4060-J 4013  smp /dev/bsg/expander-1:0
note: these are the phys of the HBA the named shelf is attached through; which phy carries which shelf is topology and is not reported here
```

The last four columns are the standard SAS link error counters: invalid
dwords, running disparity errors, losses of dword synchronisation and failed
phy resets. The first grows on a marginal cable or connector, the third on a
link that keeps dropping — the second row above is exactly such a link, and
it is answering and counted as `up`.

There are two sources, and they cost very different things:

| Source | What it gives | What it costs |
| --- | --- | --- |
| `/sys/class/sas_phy` | address, rate, state and the four counters of every phy | nothing: attribute reads, no external command |
| SMP, with `--smp` | the same for expander phys, plus the address at the far end | one `smp_rep_phy_err_log` per phy, needs smp_utils |

That is why SMP is a flag and not the default: a 38-phy expander is 38
requests through one SMP processor. For a command an operator typed that is
fine; for a scrape every fifteen seconds it is not, and the exporter does
not use SMP at all.

The counters are only read. `smp_rep_phy_err_log` has a `--zero` option that
clears what it prints; jbod-go never passes it — a diagnostic that destroys
the history of a suspect cable is worse than no diagnostic.

What the report does not claim:

- **`unknown` is not "the link is down".** The transport prints `Unknown`
  both for an empty connector and for a field the driver did not fill in,
  and sysfs offers nothing to tell them apart, so the column says `unknown`
  and not `down`. `disabled` (the transport said so) and `failed` (rate
  negotiation failed) are separate states, because they are diagnoses and
  not just "not up".
- **A counter that is not there prints as `-`.** A driver that publishes no
  link error counters has not said the link is clean. A zero would say
  exactly that.
- **A phy belongs to the HBA, not to the shelf.** Naming a shelf narrows the
  report to its host, and the note under the table says it plainly: tying a
  particular phy to a particular shelf is topology, which is the rest of
  1.3.

`capabilities` answers the same question in advance: `sas.phy`,
`sas.phy_error_counters` and `smp.phy_error_counters`, with the evidence —
how many phys this host has, how many of them publish counters, and whether
there is a bsg node to address SMP to. None of the three ever reports write
support: a rate and a state are reports of what the link did, not settings.

`--json` is available here too. A counter that is absent is `null` with the
reason next to it, and never a zero.

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

Since 1.2 the SES pages are read as well: up to four `sg_ses` calls per
shelf per pass, shelves in parallel and pages in sequence within a shelf
(they go through the same SES processor, so there is nothing to win by
queueing them at once). The threshold page is only read when the shelf
reports sensor elements.

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

Added in 1.2:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| jbod_enclosure_health | gauge | enclosure, enclosure_id, source, level | 1 on the current level; source is `hardware` or `components` |
| jbod_enclosure_components | gauge | enclosure, enclosure_id, type, health | elements per type in each condition |
| jbod_component_info | gauge | enclosure, enclosure_id, component, component_id, type, status, health | one element, always 1 |
| jbod_sensor_temperature_celsius | gauge | enclosure, enclosure_id, component, component_id, type | temperature of an enclosure element |
| jbod_sensor_voltage_volts | gauge | the same | voltage |
| jbod_sensor_current_amps | gauge | the same | current |
| jbod_sensor_*_threshold_* | gauge | the same plus threshold | the enclosure's own limit: high_critical, high_warning, low_warning, low_critical |
| jbod_slot_sas_address_info | gauge | enclosure, enclosure_id, slot, component_id, sas_address, device, block_device | the slot → SAS address → disk mapping, always 1 |

Added in 1.3:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| jbod_sas_phy_info | gauge | host, phy, port, sas_address, device_type, state, negotiated_link_rate | one phy, always 1 |
| jbod_sas_phy_up | gauge | host, phy, port, sas_address, device_type | 1 when the link negotiated a rate |
| jbod_sas_phy_negotiated_link_rate_gbps | gauge | the same | the rate; no series when there is none |
| jbod_sas_phy_invalid_dword_total | counter | the same | invalid dwords |
| jbod_sas_phy_running_disparity_error_total | counter | the same | running disparity errors |
| jbod_sas_phy_loss_of_dword_sync_total | counter | the same | losses of dword synchronisation |
| jbod_sas_phy_reset_problem_total | counter | the same | failed phy resets |

The counters are published as counters, carrying the hardware's own running
total. They restart on a phy reset, a driver reload and a reboot, and that
is precisely what Prometheus knows how to read: `rate()` and `increase()`
treat a drop as a reset and never produce a negative increase. Accumulating
them in the exporter would be wrong — it restarts too, and does not remember
the previous total.

The labels are the host and the phy, not the enclosure: a phy belongs to the
HBA. A counter the transport did not expose gets no series at all, because a
zero here would mean a clean link. The exporter reads sysfs only: SMP costs
one request per phy and stays out of the scrape.

Health is a label and never a number: a numeric scale would have to put
`unknown` somewhere, and every place is wrong. Next to `ok` it hides a shelf
nobody could read; next to `critical` it wakes somebody up over a threshold
page that is not implemented. Every level keeps a series, so a shelf that
recovers publishes a zero instead of leaving a stale critical series behind.
A reading the hardware did not report gets no series at all.

The health of the collection itself was added:

| Metric | Type | Meaning |
| --- | --- | --- |
| jbod_up | gauge | 1 when the collection completed |
| jbod_scrape_duration_seconds | gauge | duration of the last collection |
| jbod_scrape_errors_total | counter | cumulative failures per collector (enclosures, slots, disks, fans, components, sas, led) |
| jbod_snapshot_timestamp_seconds | gauge | when the collection behind this response started |
| jbod_collection_complete | gauge | 1 when every required SES page of a shelf answered and the pages agreed |
| jbod_ses_page_read | gauge | whether one SES page answered (labels: page, required) |
| jbod_enclosure_generation_changed | gauge | 1 when the pages of one pass described different configurations |
| jbod_enclosure_components_missing | gauge | declared elements no status page reported |

The exporter's own metrics come from client_golang and are gathered afresh on
every request, past the collection cache:

| Metric | Type | Meaning |
| --- | --- | --- |
| process_* | gauge/counter | CPU, memory, file descriptors and start time of the process (Linux only: read from /proc) |
| go_* | gauge/counter | goroutines, GC and runtime memory |
| jbod_build_info | gauge | the binary's version and toolchain in labels, value always 1 |
| promhttp_metric_handler_requests_total | counter | /metrics responses by status code |

`jbod_enclosure_info` is the join target for every series labelled with the
SCSI address: the address is assigned at scan time and gets reassigned, so a
dashboard that needs a stable identity joins on `enclosure` and reads
`enclosure_id`. The `id_source` label says whether that is a real identifier
(`logical`, `serial`) or the address again (`address`).

The process metrics (`process_cpu_seconds_total`,
`process_resident_memory_bytes`, `process_virtual_memory_bytes`,
`process_start_time_seconds`, `process_open_fds`, `process_max_fds`) come
from client_golang's process collector, the same one every other Prometheus
exporter uses; the Rust version got them from the prometheus crate, and
dashboards built on them would break without them. They are gathered on every
request and never cached. On a host without `/proc` (macOS, say) they are
simply absent — a missing series beats a fake zero.

The response is written by `promhttp`, so content negotiation (text 0.0.4 and
OpenMetrics), gzip and the headers Prometheus expects come with it, and none
of that is hand-written in this repository.

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
- There are `health`, `sensors` and `list --components`: the condition of a
  shelf and of its elements, the sensors with the thresholds the enclosure
  declares, and the slot → SAS address → disk mapping. The hardware verdict
  and the completeness of the poll are two separate rows of the report.
- Exactly one of --on and --off is required. An unknown device is an error.
- Metric compatibility is complete, `process_*` included — published, as in
  the Rust version, by the official Prometheus client; on top of the Rust
  version there are `jbod_up`, `jbod_scrape_duration_seconds`,
  `jbod_scrape_errors_total`, `jbod_enclosure_info`, `jbod_enclosure_slots`,
  `jbod_fan_speed_rpm`, the health and component series
  (`jbod_enclosure_health`, `jbod_enclosure_components`,
  `jbod_component_info`), the sensors with their thresholds
  (`jbod_sensor_*`), the bay mapping (`jbod_slot_sas_address_info`) and the
  completeness of the collection (`jbod_collection_complete`,
  `jbod_ses_page_read`, `jbod_snapshot_timestamp_seconds`).

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

Building needs access to the modules (`go mod download`) or a `vendor/`
directory (`make vendor`): the dependencies (client_golang, pflag and their
transitive modules) are not committed.

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

The SES pages are parsed from fixtures: configuration, enclosure status, the
join with Additional Element Status and the thresholds, including the three
spellings of an element type, the null SAS address, the overall element of a
type and a sensor without a value.

The `list`, `health` and `sensors` output and the metric text are pinned by
golden files, so an
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
  and fuzzing), `sespage.go` the SES page parsers, `ses.go` the component,
  sensor and health model, `order.go` the natural slot ordering, `exec.go`
  the tool resolution and the preflight check.
- internal/metrics — a snapshot as a `prometheus.Collector`: the series
  descriptors and the const metrics; the format itself is client_golang's.
- internal/exporter — the HTTP handler, the scrape timeout, sharing
  concurrent scrapes, the TTL cache and the registry holding the process,
  runtime and build-info collectors; the response is written by `promhttp`.
- internal/cli — argument parsing: `list.go`, `led.go`, `health.go`
  (health and sensors), `prometheus.go`, table rendering in `output.go`, the version in `version.go`, the logger in
  `log.go`.
