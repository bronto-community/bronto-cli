# Generated command reference + docs coverage gates (owner: generator).
# Included by the top-level Makefile via `-include docs/mk/*.mk`.

DOCS_REFERENCE_DIR := docs/src/content/docs/reference/commands

.PHONY: docs-reference docs-reference-check docs-coverage

# docs-reference regenerates docs/src/content/docs/reference/commands/ from
# the cobra command tree (internal/tools/docgen). Commit the result.
docs-reference:
	go run ./internal/tools/docgen -out $(DOCS_REFERENCE_DIR) -links docs/reference-links.yaml

# docs-reference-check regenerates the reference and fails if the committed
# pages differ (modified, deleted, or new untracked pages).
docs-reference-check: docs-reference
	@if ! git diff --quiet --exit-code -- $(DOCS_REFERENCE_DIR) || \
	   [ -n "$$(git ls-files --others --exclude-standard -- $(DOCS_REFERENCE_DIR))" ]; then \
		echo ""; \
		echo "docs-reference-check: the generated command reference is out of date."; \
		echo "A command, flag, or help text changed without regenerating the docs."; \
		echo "Fix: run 'make docs-reference' and commit $(DOCS_REFERENCE_DIR)/."; \
		echo ""; \
		git --no-pager diff --stat -- $(DOCS_REFERENCE_DIR); \
		git ls-files --others --exclude-standard -- $(DOCS_REFERENCE_DIR) | sed 's/^/  untracked: /'; \
		exit 1; \
	fi
	@echo "docs-reference-check: reference is up to date"

# docs-coverage runs the Go-side docs gates: the doc-rot guard (every
# `bronto ...` example is a real command/flag), guide coverage (every
# command is taught in a hand-written page), reference completeness, and
# the resource-verb-exceptions sentence.
docs-coverage:
	go test -count=1 ./internal/cli/ -run 'TestSkillDoc|TestDocChecker|TestDocsStateResourceVerbExceptions|TestDocsGuideCoverage|TestDocsReferenceComplete|TestDocsCoverageAllowlist'
