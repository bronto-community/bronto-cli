# bronto CLI docs

Source of https://bronto-cli.vercel.app, built with
[Astro Starlight](https://starlight.astro.build).

Read [AGENTS.md](./AGENTS.md) before you write or change pages. It covers the
layout, who owns which files, code block conventions (tested `console test`
snippets), VHS recordings, and the docs mock world.

## Local development

Requires the Node version in [`.nvmrc`](./.nvmrc).

```sh
make docs-dev       # dev server on http://localhost:4321
make docs-build     # production build into docs/dist (fails on broken links)
make docs-check     # everything CI runs for docs
```

## Site shell

| File | Purpose |
| --- | --- |
| `astro.config.mjs` | Starlight config: sidebar, plugins, `~` import alias |
| `ec.config.mjs` | Expressive Code: "tested" label, console copy button copies commands only |
| `src/styles/custom.css` | Bronto theme colors |
| `src/components/Tape.astro` | VHS video embed |
| `src/components/SocialIcons.astro` | Header link to docs.bronto.io |
| `src/components/routeData.ts` | Sidebar label for the generated command reference |

## Deployment

`.github/workflows/docs.yml` builds the site on every pull request and push
to `main`. It deploys a preview for same-repo pull requests (the URL is posted
as a PR comment) and deploys to production on push to `main`. The Vercel
project (`bronto-cli`, team `brontoio`) has no Git connection; all deploys
come from that workflow.
