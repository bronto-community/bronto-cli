# Agent guide

Instructions for coding agents working on this repo. [CONTRIBUTING.md](./CONTRIBUTING.md) is the full guide; this file holds what an agent must not get wrong.

## Testing against Bronto

All live testing goes to the **test org** in `.env.local`, never anywhere else. Setup: [CONTRIBUTING.md#local-test-environment](./CONTRIBUTING.md#local-test-environment).

- **CLI:** run live commands through `scripts/test-env`, e.g. `scripts/test-env bronto dashboards list`. Don't use `--profile`, a default profile, or an API key from the environment: those may point at a real org.
- **Browser:** use only the test browser (`make browser`, driven through the `bronto-test-browser` MCP server in `.mcp.json`). If it isn't logged in, ask the person to log in. Never type passwords, API keys or other credentials into a page.
- **Name what you create** with a `cli-test-` prefix, and delete only objects you created in this session. Never change or delete anything else in the org.
- **API keys:** never create, update or delete them. When you need an id from a command's output, take it with `--jq`, never by parsing text with `sed`/`grep`: a parsing slip once deleted the CI key.
- **Credentials store:** never run `bronto auth login`, `logout` or `switch`, never write to the OS keychain, and never point `HOME` at a fake directory in tests.
- **Watch the request budget:** the test org is shared. Reuse the integration suite's seed fixture instead of seeding your own data (see "The live integration suite" in CONTRIBUTING.md).

## Working in the repo

- **Lint** with the toolchain pinned in `go.mod` when your local Go is newer, e.g. `GOTOOLCHAIN=go1.26.10 make lint`. Plain `make lint` fails on newer Go.
- **Commits** follow Conventional Commits. AI attribution goes in an `Assisted-By:` trailer, never `Co-Authored-By:`: the CLA check treats a co-author as someone who must sign.
- **Generated docs:** after changing commands or flags, run `make docs-reference` and commit the result. Update `skill.md` when behavior an agent relies on changes.
- **Out-of-scope findings** get fixed now or filed as an issue, never left as a note.
- **Naming:** in prose, "Bronto" is the platform; the CLI is `bronto` (code) or "the bronto CLI", never a bare "bronto".
