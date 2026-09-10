# Contributing

This is a port of [Gandi/jbod-rs](https://github.com/Gandi/jbod-rs), so the
first question about any change is what it does to compatibility: the metric
names and label sets, the shape of the `list` tables and the option spellings
are a contract with existing dashboards, alerts and scripts. Changing them
needs a reason in the commit message.

## Before sending a patch

```sh
make lint      # gofmt gate, and golangci-lint when installed
make test      # go vet + go test -race
make cover     # coverage, if you want the number
```

CI runs the same on Go 1.25 and 1.26, plus govulncheck, a short fuzzing pass
over the parsers, cross-builds for linux/amd64 and linux/arm64 and a `.deb`
build.

## What a change is expected to carry

- **A test that fails without it.** Bug fixes get a regression test; the
  existing ones are named after what they protect (see
  `internal/jbod/order_test.go`, `internal/cli/flags_test.go`).
- **Golden files where output is involved.** The `list` tables and the metric
  text are pinned: `go test ./internal/cli/ ./internal/metrics/ -update`
  rewrites them, and the diff belongs in the review.
- **A fuzz seed when a parser changes.** Everything in
  `internal/jbod/parse.go` reads untrusted output from the hardware; the fuzz
  targets there must keep passing:
  `go test -run xxx -fuzz FuzzParseVPD80 -fuzztime 30s ./internal/jbod/`.

## Working without hardware

No JBOD is needed. Tests build a temporary sysfs tree and inject a runner
instead of calling the sg3-utils:

```go
client := jbod.New(jbod.WithSysfs(root), jbod.WithRunner(fakeRunner))
```

The CLI commands take an `Inventory` interface, so rendering can be tested
against fixed data without any parsing (see `internal/cli/golden_test.go`).
A client with an injected runner skips the tool part of the preflight check,
but still expects the sysfs root to exist and to be non-empty.

Nothing here has been verified against a real shelf. If you do run it on
hardware, say so in the pull request and paste the `list` output and the
relevant metrics: different sg3-utils versions and enclosure models are
exactly what the stubs cannot cover.

## Style

- Match the surrounding code: named struct fields, `Optional[T]` for readings
  the hardware may not report, sentinels only in the output layer.
- Comments explain why, not what. The review that produced the current shape
  is in `REFACTORING.md`, and the item numbers (A1, B5, C3, …) are referenced
  from the code where the reasoning is not local.
- Keep the dependency list at one. `spf13/pflag` is there because POSIX
  option semantics were worth it; the bar for a second dependency is the
  same.
