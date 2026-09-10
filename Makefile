.PHONY: all build test fmt lint vet cover vendor install install-files deb clean
PREFIX ?= /usr
DESTDIR ?=
# The version is stamped into the binaries instead of being kept in sync by
# hand; without a stamp they fall back to what the build recorded (F).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# The Debian package version must not carry a leading "v" and must start with
# a digit, so a "v1.2.3-4-gdeadbee" describe becomes "1.2.3-4-gdeadbee".
DEB_VERSION ?= $(patsubst v%,%,$(VERSION))
MAINTAINER ?= $(shell git config user.name) <$(shell git config user.email)>
LDFLAGS := -s -w -X github.com/kmlebedev/jbod-go/internal/cli.version=$(VERSION)
# No cgo: the binaries are meant to run on any glibc/musl host of the same
# architecture, including a minimal container.
export CGO_ENABLED = 0
GO ?= go

all: build

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
	cd dist/package && find . -type f ! -path './DEBIAN/*' -printf '%P\0' \
	    | xargs -0 md5sum > DEBIAN/md5sums
	dpkg-deb --root-owner-group --build dist/package dist/jbod-go_$(DEB_VERSION)_$$(dpkg --print-architecture).deb
	cd dist && sha256sum *.deb > SHA256SUMS

clean:
	rm -rf bin dist coverage.out
