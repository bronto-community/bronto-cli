// @ts-check
import { defineConfig } from 'astro/config';
import { fileURLToPath } from 'node:url';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import starlightLlmsTxt from 'starlight-llms-txt';

const repo = 'https://github.com/bronto-community/bronto-cli';
const platformDocs = 'https://docs.bronto.io';

// Versioned docs: when the CLI needs docs per release, add the
// `starlight-versions` plugin to `plugins` below. The layout already fits
// what it expects: all pages live under src/content/docs/ and the sidebar is
// built only from `autogenerate` groups (no hard-coded slugs), so the plugin
// can copy the tree into a versioned directory and reuse the sidebar config.

// https://astro.build/config
export default defineConfig({
	site: 'https://bronto-cli.vercel.app',
	vite: {
		resolve: {
			// `~/components/Tape.astro` etc. Keep in sync with tsconfig.json `paths`.
			alias: { '~': fileURLToPath(new URL('./src', import.meta.url)) },
		},
	},
	integrations: [
		starlight({
			title: 'bronto CLI',
			description:
				'Search logs, tail streams, inspect traces, and manage Bronto resources from the terminal.',
			logo: { src: './src/assets/logomark.png', alt: 'Bronto' },
			favicon: '/favicon.png',
			head: [
				{ tag: 'link', attrs: { rel: 'apple-touch-icon', href: '/apple-touch-icon.png' } },
			],
			social: [{ icon: 'github', label: 'GitHub', href: repo }],
			editLink: { baseUrl: `${repo}/edit/main/docs/` },
			customCss: ['./src/styles/custom.css'],
			routeMiddleware: './src/components/routeData.ts',
			components: {
				SocialIcons: './src/components/SocialIcons.astro',
			},
			sidebar: [
				{ label: 'Start here', items: [{ autogenerate: { directory: 'getting-started' } }] },
				{ label: 'Guides', items: [{ autogenerate: { directory: 'guides' } }] },
				{ label: 'Configuration', items: [{ autogenerate: { directory: 'configuration' } }] },
				{ label: 'Extend', items: [{ autogenerate: { directory: 'extend' } }] },
				{ label: 'Troubleshooting', slug: 'troubleshooting' },
				{
					// Hand-written reference pages, plus the generated reference/commands/
					// subgroup, which starts collapsed (src/components/routeData.ts labels it).
					label: 'Reference',
					items: [{ autogenerate: { directory: 'reference', collapsed: true } }],
				},
				{
					label: 'Bronto platform docs',
					link: platformDocs,
					attrs: { target: '_blank', rel: 'noopener' },
				},
			],
			plugins: [
				// Fails the build on broken internal links and anchors. Write
				// internal links as absolute paths (/guides/search/), not ./relative.
				starlightLinksValidator(),
				// Generates /llms.txt, /llms-full.txt and /llms-small.txt for agents.
				starlightLlmsTxt({
					projectName: 'bronto CLI',
					description:
						'`bronto` is the command-line interface for the Bronto logging platform. These docs cover using the CLI; platform concepts are documented at https://docs.bronto.io.',
					optionalLinks: [
						{
							label: 'Bronto platform docs',
							url: platformDocs,
							description: 'Datasets, collections, the query language, monitors, API keys.',
						},
					],
					demote: ['reference/commands/**'],
				}),
			],
		}),
	],
});
