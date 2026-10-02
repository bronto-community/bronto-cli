# Tested snippets + VHS tapes (owner: tapes). Included by the top-level
# Makefile via `-include docs/mk/*.mk`.
#
# Both run the bronto binary built from this tree against the deterministic
# docs mock server (internal/tools/docsmock, fixtures in docs/testdata/mock/).

.PHONY: docs-snippets docs-snippets-print docs-tapes docs-tapes-check docs-tapes-live

# docs-snippets runs every ```console test block in docs/src/content/docs
# against the mock (internal/tools/docsnippets). Failures name file:line,
# the command, and a diff.
docs-snippets:
	go test -count=1 ./internal/tools/docsnippets/

# docs-snippets-print prints every block with its actual output filled in,
# ready to paste back into the page.
docs-snippets-print:
	DOCS_SNIPPETS_PRINT=1 go test -count=1 -run TestDocSnippets ./internal/tools/docsnippets/

# docs-tapes renders every docs/tapes/*.tape against the mock and updates
# docs/public/tapes/<name>.{webm,png} and docs/tapes/golden/<name>.txt.
# TAPES="quickstart search" renders a subset.
docs-tapes:
	docs/tapes/record.sh $(TAPES)

# docs-tapes-check renders the tapes into a temp dir and compares only the
# text goldens. Needs vhs, ttyd, and ffmpeg.
docs-tapes-check:
	@command -v vhs >/dev/null 2>&1 || { \
		echo "docs-tapes-check: vhs is not installed."; \
		echo "  macOS: brew install vhs"; \
		echo "  Linux: https://github.com/charmbracelet/vhs#installation (vhs + ttyd + ffmpeg)"; \
		exit 1; }
	@docs/tapes/record.sh --check $(TAPES) || { \
		echo ""; \
		echo "docs-tapes-check: a tape's output no longer matches its golden."; \
		echo "Fix: run 'make docs-tapes' and commit docs/tapes/golden/ and docs/public/tapes/."; \
		exit 1; }

# docs-tapes-live re-records the videos against the live CI test account
# (BRONTO_IT_MGMT_KEY, BRONTO_IT_INGEST_KEY, BRONTO_IT_REGION). Videos and
# posters only; goldens stay mock-based. See docs/tapes/record.sh.
docs-tapes-live:
	DOCS_TARGET=live docs/tapes/record.sh $(TAPES)
