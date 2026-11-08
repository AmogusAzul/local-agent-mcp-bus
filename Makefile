WIN_USER ?= $(shell cmd.exe /c "echo %USERNAME%" 2>/dev/null | tr -d '\r')
INSTALL_DIR ?= /mnt/c/Users/$(WIN_USER)/tools

.PHONY: build-windows install

build-windows:
	./build.sh

install: build-windows
	mkdir -p $(INSTALL_DIR)
	for f in bin/*.exe; do cmp -s $$f $(INSTALL_DIR)/$$(basename $$f) || cp $$f $(INSTALL_DIR)/; done
	./register-claude-desktop.sh $(INSTALL_DIR)
