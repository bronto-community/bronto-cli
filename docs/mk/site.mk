# Docs site shell (docs/, Mintlify). Included by the top-level Makefile.

.PHONY: docs-install docs-validate docs-dev

# docs-install installs the exact locked dependencies (the mint CLI).
docs-install:
	npm --prefix docs ci

# docs-validate builds the site in strict mode (fails on MDX errors and
# docs.json problems) and checks internal links and their #anchors.
docs-validate: docs-install
	cd docs && npx --no-install mint validate
	cd docs && npx --no-install mint broken-links --check-anchors

# docs-dev runs the local preview with live reload (http://localhost:3000).
docs-dev:
	@test -d docs/node_modules || npm --prefix docs ci
	cd docs && npx --no-install mint dev
