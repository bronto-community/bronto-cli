// @ts-check
// Expressive Code config. It lives in its own file (not in astro.config.mjs)
// so the <Code> component and Markdown code fences share the same plugins.
import { defineEcConfig } from '@astrojs/starlight/expressive-code';
import { selectAll, select, h, addClassName } from '@astrojs/starlight/expressive-code/hast';

/**
 * Handles ```console code blocks:
 *
 * - The copy button copies only the commands: lines starting with `$ ` (minus
 *   the prompt). Expected output lines are dropped. Expressive Code would
 *   otherwise copy the whole block, prompts included.
 * - Blocks that also have the `test` meta word (```console test) get a small
 *   "tested" label. The snippet tester (make docs-snippets) runs these
 *   against the docs mock server.
 *
 * It uses the block-group hook so it runs after the frames plugin has added
 * the copy button and header.
 * @returns {import('@astrojs/starlight/expressive-code').ExpressiveCodePlugin}
 */
function pluginConsoleSnippets() {
	return {
		name: 'bronto-console-snippets',
		baseStyles: `
			.bronto-tested {
				margin-inline-start: auto;
				padding: 0.05rem 0.45rem;
				border: 1px solid currentColor;
				border-radius: 999px;
				font-size: 0.7rem;
				line-height: 1.4;
				letter-spacing: 0.03em;
				text-transform: uppercase;
				opacity: 0.75;
				white-space: nowrap;
				align-self: center;
			}
			.frame.is-terminal .header:has(.bronto-tested) { display: flex; }
		`,
		hooks: {
			postprocessRenderedBlockGroup: ({ renderedGroupContents }) => {
				for (const { codeBlock, renderedBlockAst } of renderedGroupContents) {
					if (codeBlock.language !== 'console') continue;

					// Copy only the commands: "$ " lines, plus the lines that
					// continue them after a trailing backslash. Expected output
					// is left out.
					const commands = [];
					let continuing = false;
					for (const { text } of codeBlock.getLines()) {
						if (text.startsWith('$ ')) {
							commands.push(text.slice(2));
						} else if (continuing) {
							commands[commands.length - 1] += '\n' + text;
						} else {
							continue;
						}
						continuing = text.trimEnd().endsWith('\\');
					}
					if (commands.length > 0) {
						for (const button of selectAll('.copy button', renderedBlockAst)) {
							button.properties.dataCode = commands.join('\x7F');
						}
					}

					const tested = /(^|\s)test(\s|$)/.test(codeBlock.meta);
					const header = select('figcaption.header', renderedBlockAst);
					if (tested && header) {
						addClassName(renderedBlockAst, 'bronto-tested-block');
						header.children.push(
							h(
								'span',
								{
									className: ['bronto-tested'],
									title: 'This snippet runs in CI against the docs mock server',
								},
								'tested',
							),
						);
					}
				}
			},
		},
	};
}

export default defineEcConfig({
	plugins: [pluginConsoleSnippets()],
});
