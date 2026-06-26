import type { Root } from 'hast'
import type { HighlighterCore } from 'shiki/core'
import rehypeShikiFromHighlighter from '@shikijs/rehype/core'
// Import themes
import themeGithubDark from '@shikijs/themes/github-dark'
import themeGithubLight from '@shikijs/themes/github-light'
import rehypeStringify from 'rehype-stringify'
import remarkGfm from 'remark-gfm'
import remarkParse from 'remark-parse'
import remarkRehype from 'remark-rehype'
// Import bundled language grammars
import langBash from 'shiki/langs/bash.mjs'
import langC from 'shiki/langs/c.mjs'
import langCpp from 'shiki/langs/cpp.mjs'
import langCss from 'shiki/langs/css.mjs'
import langDiff from 'shiki/langs/diff.mjs'
import langGo from 'shiki/langs/go.mjs'
import langHtml from 'shiki/langs/html.mjs'
import langJava from 'shiki/langs/java.mjs'
import langJavascript from 'shiki/langs/javascript.mjs'
import langJson from 'shiki/langs/json.mjs'
import langJsx from 'shiki/langs/jsx.mjs'
import langMarkdown from 'shiki/langs/markdown.mjs'
import langPython from 'shiki/langs/python.mjs'
import langRust from 'shiki/langs/rust.mjs'
import langSql from 'shiki/langs/sql.mjs'
import langToml from 'shiki/langs/toml.mjs'
import langTsx from 'shiki/langs/tsx.mjs'
import langTypescript from 'shiki/langs/typescript.mjs'
import langXml from 'shiki/langs/xml.mjs'
import langYaml from 'shiki/langs/yaml.mjs'
import { unified } from 'unified'
import { visit } from 'unist-util-visit'

/**
 * The remark+rehype+Shiki markdown pipeline configuration, shared by BOTH the
 * main-thread synchronous renderer (renderMarkdown.ts, which holds a
 * createHighlighterCoreSync instance) and the off-thread worker
 * (markdownWorker.ts, which holds a createHighlighterCore instance). Centralizing
 * the plugin chain + theme/lang set here is load-bearing: the worker's HTML output
 * MUST be byte-identical to the sync path's, because the same `markdownCache`
 * serves both and the CSS themes against the exact `pre.shiki` structure Shiki
 * emits (markdownContent.css.ts). A divergence would flash a differently-styled
 * block when the async result swaps in, or break theming outright.
 */

/**
 * Both Shiki themes with their `bg` overridden to `transparent`, so Shiki emits
 * `--shiki-light-bg`/`--shiki-dark-bg` as transparent and the surrounding
 * wrapper's background shows through instead of the theme's editor color.
 */
export const transparentBgThemes = [themeGithubLight, themeGithubDark].map(t => ({ ...t, bg: 'transparent' }))

/** The bundled Shiki language grammars, shared by the sync highlighter and the worker. */
export const shikiLangs = [
  langTypescript,
  langJavascript,
  langPython,
  langRust,
  langGo,
  langJava,
  langBash,
  langJson,
  langHtml,
  langCss,
  langYaml,
  langToml,
  langSql,
  langMarkdown,
  langDiff,
  langC,
  langCpp,
  langJsx,
  langTsx,
  langXml,
]

const HTTP_URL_RE = /^https?:\/\//

/** Rehype plugin that secures links: adds target/rel to http(s) links, unwraps non-http(s) links. */
function rehypeExternalLinks() {
  return (tree: Root) => {
    visit(tree, 'element', (node, index, parent) => {
      if (node.tagName !== 'a')
        return
      const href = node.properties?.href
      if (typeof href === 'string' && HTTP_URL_RE.test(href)) {
        node.properties ??= {}
        node.properties.target = '_blank'
        node.properties.rel = 'noopener noreferrer nofollow'
      }
      else if (parent && typeof index === 'number') {
        // Non-http(s) link — unwrap: replace <a> with its children
        parent.children.splice(index, 1, ...node.children)
        return index
      }
    })
  }
}

/**
 * Build the full markdown->HTML processor (remark + GFM + rehype + Shiki + link
 * hardening) around a Shiki highlighter instance. Takes the highlighter so the
 * main thread can pass its synchronous instance and the worker its own — the rest
 * of the chain (and thus the output) is identical.
 */
export function createMarkdownProcessor(highlighter: HighlighterCore) {
  return unified()
    .use(remarkParse)
    .use(remarkGfm)
    .use(remarkRehype)
    .use(rehypeShikiFromHighlighter, highlighter as Parameters<typeof rehypeShikiFromHighlighter>[0], {
      themes: { light: 'github-light', dark: 'github-dark' },
      defaultColor: false,
    })
    .use(rehypeExternalLinks)
    .use(rehypeStringify)
}

/**
 * Markdown->HTML processor WITHOUT Shiki: the fast synchronous placeholder render
 * (used while the worker's highlighted result is in flight) and the fallback when
 * Shiki throws. Code blocks render as plain `<pre><code class="language-x">` —
 * container-styled but not theme-colored until the highlighted result swaps in.
 */
export const plainMarkdownProcessor = unified()
  .use(remarkParse)
  .use(remarkGfm)
  .use(remarkRehype)
  .use(rehypeExternalLinks)
  .use(rehypeStringify)

/**
 * Render `text` through `processor` (highlighted), degrading to the plain
 * (un-highlighted) processor when Shiki throws on a grammar (e.g. an unsupported
 * lookbehind in Safari's regex engine). Single-sources the "on Shiki failure, fall
 * back to plain" rule that BOTH the main-thread synchronous path and the worker
 * apply, so the two can't drift on what counts as a fallback.
 */
export function renderWithPlainFallback(
  processor: ReturnType<typeof createMarkdownProcessor>,
  text: string,
): string {
  try {
    return String(processor.processSync(text))
  }
  catch {
    return String(plainMarkdownProcessor.processSync(text))
  }
}
