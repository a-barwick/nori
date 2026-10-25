.PHONY: dev web core check

dev:
	$(MAKE) -j2 web core

web:
	cd web && ./node_modules/.bin/next dev -H 127.0.0.1 --port 3210

core:
	$(MAKE) -C core run

check:
	$(MAKE) -C core check
	cd web && ./node_modules/.bin/tsc --noEmit -p . && ./node_modules/.bin/eslint src
