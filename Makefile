.PHONY: all build test fmt lint vet cover vendor install install-files deb clean
PREFIX ?= /usr
DESTDIR ?=
# The version is stamped into the binaries instead of being kept in sync by
# hand; without a stamp they fall back to what the build recorded (F).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# A Debian version has to start with a digit. "v1.2.3-4-gdeadbee" loses its
# "v", but a checkout with no tags gives a bare commit hash ("f70d531") and a
# tree with no git gives "dev": both become 0.0.0+<what git said>, which sorts
# below any real release.
# The "v" is stripped with sed rather than with a shell suffix expansion: a
# literal hash inside a make variable starts a comment and would swallow the
# rest of the line.
DEB_VERSION ?= $(shell v=$$(printf '%s' '$(VERSION)' | sed 's/^v//'); if printf '%s' "$$v" | grep -qE '^[0-9]'; then printf '%s' "$$v"; else printf '0.0.0+%s' "$$v"; fi)
# The maintainer comes from the git identity when there is one. A CI runner
# has none, so a build meant for distribution should pass its own:
#   make deb MAINTAINER="Name <mail@example.org>"
MAINTAINER ?= $(shell name=$$(git config user.name 2>/dev/null); 	mail=$$(git config user.email 2>/dev/null); 	if [ -n "$$name" ] && [ -n "$$mail" ]; 	then printf '%s <%s>' "$$name" "$$mail"; 	else printf 'jbod-go maintainers <maintainers@example.invalid>'; fi)
LDFLAGS := -s -w -X github.com/kmlebedev/jbod-go/internal/cli.version=$(VERSION)
# No cgo: the binaries are meant to run on any glibc/musl host of the same
# architecture, including a minimal container.
export CGO_ENABLED = 0
GO ?= go

all: build

build_linux:
	mkdir -p bin
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/jbod-linux ./cmd/jbod
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/prometheus-jbod-exporter-linux ./cmd/prometheus-jbod-exporter

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/jbod ./cmd/jbod
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/prometheus-jbod-exporter ./cmd/prometheus-jbod-exporter

test: vet
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

# lint fails when anything is unformatted, then runs golangci-lint if it is
# installed. CI runs the same gate.
lint:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; \
	else echo "golangci-lint not installed, skipping"; fi

cover:
	$(GO) test -covermode=atomic -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

# vendor is for building the .deb offline: the only dependency is pflag.
vendor:
	$(GO) mod vendor

install: build install-files

# install-files has no build dependency, so "deb" does not build twice.
install-files:
	install -d $(DESTDIR)$(PREFIX)/bin $(DESTDIR)/lib/systemd/system $(DESTDIR)/etc/default
	install -m 0755 bin/jbod bin/prometheus-jbod-exporter $(DESTDIR)$(PREFIX)/bin/
	install -m 0644 debian/prometheus-jbod-exporter.service $(DESTDIR)/lib/systemd/system/
	install -m 0644 debian/prometheus-jbod-exporter.default $(DESTDIR)/etc/default/prometheus-jbod-exporter

deb: build
	@# dpkg-deb reports these as a parse error in a generated file; saying it
	@# here names the variable to fix instead.
	@case '$(DEB_VERSION)' in [0-9]*) ;; 	*) echo 'deb: Debian version "$(DEB_VERSION)" does not start with a digit; pass DEB_VERSION=' >&2; exit 1 ;; esac
	@case '$(MAINTAINER)' in *'<'*'@'*'>') ;; 	*) echo 'deb: MAINTAINER must be "Name <mail@host>", got "$(MAINTAINER)"' >&2; exit 1 ;; esac
	rm -rf dist/package
	mkdir -p dist/package/DEBIAN dist/package/usr/share/doc/jbod-go
	$(MAKE) install-files DESTDIR=$(CURDIR)/dist/package PREFIX=/usr
	cp LICENSE dist/package/usr/share/doc/jbod-go/copyright
	sed -e "s/@VERSION@/$(DEB_VERSION)/" \
	    -e "s|@MAINTAINER@|$(MAINTAINER)|" \
	    -e "s|@DATE@|$$(date -R)|" debian/changelog \
	    > dist/package/usr/share/doc/jbod-go/changelog.Debian
	gzip -9n dist/package/usr/share/doc/jbod-go/changelog.Debian
	sed -e "s/@ARCH@/$$(dpkg --print-architecture)/" \
	    -e "s/@VERSION@/$(DEB_VERSION)/" \
	    -e "s|@MAINTAINER@|$(MAINTAINER)|" debian/control > dist/package/DEBIAN/control
	install -m 0755 debian/postinst debian/prerm debian/postrm dist/package/DEBIAN/
	echo /etc/default/prometheus-jbod-exporter > dist/package/DEBIAN/conffiles
	@# -printf is GNU-only; -exec keeps this working wherever find is POSIX.
	cd dist/package && find . -type f ! -path './DEBIAN/*' -exec md5sum {} + \
	    | sed 's|  \./|  |' > DEBIAN/md5sums
	dpkg-deb --root-owner-group --build dist/package dist/jbod-go_$(DEB_VERSION)_$$(dpkg --print-architecture).deb
	cd dist && sha256sum *.deb > SHA256SUMS

clean:
	rm -rf bin dist coverage.out
