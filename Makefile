# Voice Memories Curator (vmc)
#
# Local workflow:
#   make test          # go test ./...
#   make install       # ~/.local/bin + share scripts (fix + Hub upload)
#   make permissions   # FDA app bundle
#
# Homebrew HEAD (after push to main):
#   make brew-reinstall
#   make brew-restart
#   make logs          # tail service log
#
# Dataset: partitioned data/*.parquet — load with streaming=True
#   from datasets import load_dataset
#   ds = load_dataset("USER/voice-memories", split="train", streaming=True)
#
# Service log: $(brew --prefix)/var/log/vmc.log

PREFIX     ?= $(HOME)/.local
BREW_PREFIX := $(shell brew --prefix 2>/dev/null)
TAP        ?= jborkowski/vmc/vmc
LOG        := $(if $(BREW_PREFIX),$(BREW_PREFIX)/var/log/vmc.log,/opt/homebrew/var/log/vmc.log)

.PHONY: all test build run clean install permissions \
	brew-reinstall brew-restart brew-status logs help check

all: test build

help:
	@echo "Targets:"
	@echo "  make test            go test ./..."
	@echo "  make build           CGO binary ./vmc"
	@echo "  make install         install binary + share scripts to $(PREFIX)"
	@echo "  make permissions     open Full Disk Access"
	@echo "  make brew-reinstall  brew uninstall + install --HEAD $(TAP)"
	@echo "  make brew-restart    brew services restart vmc"
	@echo "  make brew-status     brew services info + vmc status"
	@echo "  make logs            tail -f $(LOG)"
	@echo "  make check           test + build (pre-push)"
	@echo ""
	@echo "Hub: partitioned data/*.parquet (Audio feature). streaming=True for iterable."
	@echo "Brew share scripts: fix_hf_parquet.py, upload_hf_shards.py"
	@echo "Service log: $(LOG)"

check: test build

test:
	CGO_ENABLED=1 go test -count=1 ./...

build:
	CGO_ENABLED=1 go build -o vmc .

run: build
	./vmc

# Local install (dev). Brew formula also installs these under share/vmc.
install: build
	install -d $(PREFIX)/bin
	install -d $(PREFIX)/share/vmc
	install -m 755 vmc $(PREFIX)/bin/vmc
	install -m 755 scripts/grant-fda.sh $(PREFIX)/bin/vmc-grant-fda
	install -m 755 scripts/vmc-service.sh $(PREFIX)/bin/vmc-service
	install -m 644 scripts/fix_hf_parquet.py $(PREFIX)/share/vmc/fix_hf_parquet.py
	install -m 644 scripts/upload_hf_shards.py $(PREFIX)/share/vmc/upload_hf_shards.py
	@echo "Installed $(PREFIX)/bin/vmc"
	@echo "Share scripts: fix_hf_parquet.py upload_hf_shards.py → $(PREFIX)/share/vmc/"
	@echo "Ensure PATH includes $(PREFIX)/bin"

permissions:
	bash scripts/grant-fda.sh

# Requires: changes pushed to origin/main (formula is HEAD).
brew-reinstall:
	brew tap jborkowski/vmc https://github.com/jborkowski/voice-memories-curator 2>/dev/null || true
	brew uninstall --ignore-dependencies vmc 2>/dev/null || true
	brew update-reset "$$(brew --repo jborkowski/vmc)" 2>/dev/null || true
	brew install --HEAD --formula $(TAP)
	@echo "Installed HEAD. Share has upload_hf_shards.py + fix_hf_parquet.py."
	@echo "Logs: $(LOG)"
	@echo "Next: make brew-restart && make permissions"

brew-restart:
	brew services restart vmc
	@echo "Service log: $(LOG)"

brew-status:
	brew services info vmc || true
	vmc status || true
	@echo "---"
	@echo "Log file: $(LOG)"
	@ls -la "$(LOG)" 2>/dev/null || echo "(log empty or missing until first service run)"

logs:
	@echo "Tailing $(LOG) (Ctrl-C to stop)"
	@mkdir -p "$$(dirname "$(LOG)")"
	@touch "$(LOG)"
	tail -f "$(LOG)"

clean:
	rm -f vmc
