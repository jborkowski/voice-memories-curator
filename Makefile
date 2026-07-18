.PHONY: build run clean install permissions

build:
	CGO_ENABLED=1 go build -o vmc .

run: build
	./vmc

install: build
	install -d $(HOME)/.local/bin
	install -d $(HOME)/.local/share/vmc
	install -m 755 vmc $(HOME)/.local/bin/vmc
	install -m 644 scripts/fix_hf_parquet.py $(HOME)/.local/share/vmc/fix_hf_parquet.py

permissions:
	open "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_AllFiles"

clean:
	rm -f vmc
