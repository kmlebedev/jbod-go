.PHONY: build test install deb clean
PREFIX ?= /usr
DESTDIR ?=
build:
	mkdir -p bin
	go build -trimpath -o bin/jbod ./cmd/jbod
	go build -trimpath -o bin/prometheus-jbod-exporter ./cmd/prometheus-jbod-exporter
test:
	go test -race ./...
	go vet ./...
install: build
	install -d $(DESTDIR)$(PREFIX)/bin $(DESTDIR)/lib/systemd/system
	install -m 0755 bin/jbod bin/prometheus-jbod-exporter $(DESTDIR)$(PREFIX)/bin/
	install -m 0644 debian/prometheus-jbod-exporter.service $(DESTDIR)/lib/systemd/system/
deb: build
	mkdir -p dist/package/DEBIAN dist/package/usr/share/doc/jbod-go
	$(MAKE) install DESTDIR=$(CURDIR)/dist/package PREFIX=/usr
	cp LICENSE dist/package/usr/share/doc/jbod-go/copyright
	sed "s/@ARCH@/$$(dpkg --print-architecture)/" debian/control > dist/package/DEBIAN/control
	dpkg-deb --root-owner-group --build dist/package dist/jbod-go.deb
clean:
	rm -rf bin dist
