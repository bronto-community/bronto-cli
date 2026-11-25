# Generated command reference + docs coverage gates (owner: generator).
# Included by the top-level Makefile via `-include docs/mk/*.mk`.

DOCS_REFERENCE_DIR := docs/cli/reference/commands
DOCS_API_COVERAGE := docs/cli/reference/api-coverage.mdx
# Everything docs-reference writes.
DOCS_GENERATED := $(DOCS_REFERENCE_DIR) $(DOCS_API_COVERAGE)

.PHONY: docs-reference docs-reference-check docs-coverage

# docs-reference regenerates docs/cli/reference/commands/ from the cobra
# command tree (internal/tools/docgen), and the API coverage page from
# api/openapi.yaml and internal/cli/apicoverage.go. Commit the result.
docs-reference:
	go run ./internal/tools/docgen -out $(DOCS_REFERENCE_DIR) -links docs/reference-links.yaml \
		-spec api/openapi.yaml -api-coverage $(DOCS_API_COVERAGE)

# docs-reference-check regenerates the reference and fails if the committed
# pages differ (modified, deleted, or new untracked pages).
docs-reference-check: docs-reference
	@if ! git diff --quiet --exit-code -- $(DOCS_GENERATED) || \
	   [ -n "$$(git ls-files --others --exclude-standard -- $(DOCS_GENERATED))" ]; then \
		echo ""; \
		echo "docs-reference-check: the generated command reference is out of date."; \
		echo "A command, flag, help text, or the API spec changed without regenerating the docs."; \
		echo "Fix: run 'make docs-reference' and commit $(DOCS_GENERATED)."; \
		echo ""; \
		git --no-pager diff --stat -- $(DOCS_GENERATED); \
		git ls-files --others --exclude-standard -- $(DOCS_GENERATED) | sed 's/^/  untracked: /'; \
		exit 1; \
	fi
	@echo "docs-reference-check: reference is up to date"

# docs-coverage runs the Go-side docs gates: the doc-rot guard (every
# `bronto ...` example is a real command/flag), guide coverage (every
# command is taught in a hand-written page), reference completeness, and
# the resource-verb-exceptions sentence.
docs-coverage:
	go test -count=1 ./internal/cli/ -run 'TestSkillDoc|TestDocChecker|TestDocsStateResourceVerbExceptions|TestDocsGuideCoverage|TestDocsReferenceComplete|TestDocsCoverageAllowlist'
