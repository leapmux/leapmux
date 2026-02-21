import type { Root } from 'hast'
import rehypeShikiFromHighlighter from '@shikijs/rehype/core'
// Import themes
import themeGithubDark from '@shikijs/themes/github-dark'
import themeGithubLight from '@shikijs/themes/github-light'
import rehypeStringify from 'rehype-stringify'
import remarkGfm from 'remark-gfm'
import remarkParse from 'remark-parse'
import remarkRehype from 'remark-rehype'

import { createHighlighterCoreSync } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
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

// Create synchronous Shiki highlighter with pre-loaded languages
export const shikiHighlighter = createHighlighterCoreSync({
  themes: [themeGithubLight, themeGithubDark],
  langs: [
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
  ],
  engine: createJavaScriptRegexEngine(),
})

/** Rehype plugin that secures links: adds target/rel to http(s) links, unwraps non-http(s) links. */
function rehypeExternalLinks() {
  return (tree: Root) => {
    visit(tree, 'element', (node, index, parent) => {
      if (node.tagName !== 'a')
        return
      const href = node.properties?.href
      if (typeof href === 'string' && /^https?:\/\//.test(href)) {
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

const processor = unified()
  .use(remarkParse)
  .use(remarkGfm)
  .use(remarkRehype)
  .use(rehypeShikiFromHighlighter, shikiHighlighter as any, {
    themes: { light: 'github-light', dark: 'github-dark' },
    defaultColor: false,
  })
  .use(rehypeExternalLinks)
  .use(rehypeStringify)

// LRU cache for rendered markdown: avoids re-running the full remark+shiki pipeline
// for identical content (e.g. on tab switch, thread expand/collapse, re-mount).
const CACHE_MAX_SIZE = 256
const markdownCache = new Map<string, string>()

export function renderMarkdown(text: string, skipCache = false): string {
  if (!skipCache) {
    const cached = markdownCache.get(text)
    if (cached !== undefined) {
      // Move to end (most recently used) by re-inserting
      markdownCache.delete(text)
      markdownCache.set(text, cached)
      return cached
    }
  }

  const result = String(processor.processSync(text))

  if (!skipCache) {
    // Evict oldest entry if at capacity
    if (markdownCache.size >= CACHE_MAX_SIZE) {
      const firstKey = markdownCache.keys().next().value!
      markdownCache.delete(firstKey)
    }
    markdownCache.set(text, result)
  }

  return result
}
