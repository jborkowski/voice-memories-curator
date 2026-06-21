.PHONY: build run clean install permissions

build:
	CGO_ENABLED=1 go build -o vmc .

run: build
	./vmc

install: build
	install -d $(HOME)/.local/bin
	install -m 755 vmc $(HOME)/.local/bin/vmc

permissions:
	open "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_AllFiles"

clean:
	rm -f vmc
