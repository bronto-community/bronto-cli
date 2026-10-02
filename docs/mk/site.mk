# Docs site shell (docs/, Astro Starlight). Included by the top-level Makefile.

.PHONY: docs-install docs-build docs-dev

# docs-install installs the exact locked dependencies.
docs-install:
	npm --prefix docs ci

# docs-build builds the static site into docs/dist. starlight-links-validator
# fails the build on broken internal links.
docs-build: docs-install
	npm --prefix docs run build

# docs-dev runs the dev server with live reload (http://localhost:4321).
docs-dev:
	@test -d docs/node_modules || npm --prefix docs ci
	npm --prefix docs run dev
