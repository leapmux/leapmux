import type { Root } from 'hast'
import type { Root as MdastRoot } from 'mdast'
import type { HighlighterCore } from 'shiki/core'
import type { Processor } from 'unified'
import rehypeShikiFromHighlighter from '@shikijs/rehype/core'
import rehypeStringify from 'rehype-stringify'
import remarkGfm from 'remark-gfm'
import remarkParse from 'remark-parse'
import remarkRehype from 'remark-rehype'
import { unified } from 'unified'
import { visit } from 'unist-util-visit'
import { rehypeBlockRemoteImages } from './rehypeBlockRemoteImages'
import { shikiStyleClassTransformer } from './shikiStyleClass'
import { DUAL_THEME_TOKEN_OPTIONS } from './shikiThemes'

/**
 * The remark+rehype+Shiki markdown pipeline configuration, shared by BOTH the
 * main-thread synchronous renderer (renderMarkdown.ts, which holds a
 * createHighlighterCoreSync instance on the JS regex engine) and the off-thread
 * worker (markdownWorker.ts, which holds a createHighlighterCore instance on the
 * Oniguruma WASM engine, lazily loading grammars). Centralizing the plugin chain +
 * theme set here is load-bearing: both paths emit the same themed `pre.shiki`
 * structure the CSS targets (markdownContent.css.ts), with `--shiki-light` /
 * `--shiki-dark` dual-theme variables.
 *
 * The two paths may now use DIFFERENT engines, so their token boundaries can
 * differ in edge cases -- but they never coexist in one runtime (the worker runs
 * in the browser; the sync path runs only when `Worker` is undefined, i.e.
 * tests/SSR), so the shared `markdownCache` is filled by exactly one of them per
 * session and there is no flash from a sync->worker swap.
 *
 * The eager 20-grammar set the sync fallback needs lives with its sole consumer in
 * renderMarkdown.ts (`shikiLangs`), NOT here -- so the lazy worker/editor paths that
 * import this module for the pipeline factory don't drag in 20 eager grammar chunks
 * they never use.
 */

// Case-sensitive on purpose: only a lowercase `http(s)://` scheme is treated as
// an external link (given target=_blank + a safe rel below). Anything else --
// including a mixed-case scheme like `HttPs://`, which IS a valid http URL under
// RFC 3986 -- falls through to the unwrap branch and becomes plain text. That is
// the conservative outcome for a largely agent-authored (and thus
// prompt-injectable) document: a scheme we did not recognize as external is not
// turned INTO an external link. The paired image blocker
// (rehypeBlockRemoteImages) uses a case-insensitive scheme test, so the two end
// up handling mixed-case schemes the same conservative way via different routes.
const HTTP_URL_RE = /^https?:\/\//

/**
 * Remark plugin: lower-case fenced-code languages so a mixed-case fence (```JSON,
 * ```Py) resolves to Shiki's all-lowercase grammar ids instead of degrading to a
 * plain `text` block.
 *
 * Shiki's `codeToTokens`/`codeToHast` look the language up case-sensitively, so a
 * `language-JSON` class throws "Language `JSON` not found" -- which `onError` +
 * `fallbackLanguage` then silently render plain, even though the grammar IS loaded.
 * The worker already lower-cases the fence languages it pre-loads (extractFenceLanguages),
 * and the token worker / editor parser feed `codeToTokens` the lower-cased
 * `resolveBundledLang` result; this keeps the markdown fence path consistent with both.
 */
function remarkLowercaseCodeLang() {
  return (tree: MdastRoot) => {
    visit(tree, 'code', (node) => {
      if (node.lang)
        node.lang = node.lang.toLowerCase()
    })
  }
}

/**
 * Rehype plugin that secures links: adds target/rel to http(s) links, unwraps non-http(s)
 * links. Also the single source of the link-hardening rule for the placeholder anchors
 * rehypeBlockRemoteImages emits, which is why it runs AFTER that plugin.
 */
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
 * Append the shared security-hardening + stringify tail every markdown pipeline
 * ends with: block remote images, then harden links, then stringify. Runs
 * rehypeExternalLinks AFTER rehypeBlockRemoteImages so the blocked-image
 * placeholder's `<a href>` is owned by the link-hardening pass.
 *
 * Centralizing the tail makes remote-image blocking a PROPERTY of any pipeline built
 * here, not a line each pipeline author must remember: a remote `<img>` is an
 * outbound request the page makes on its own, and agent-authored markdown is
 * prompt-injectable, so a render path that forgot the blocker would exfiltrate
 * conversation content and the user's IP. A future third render path cannot forget
 * it as long as it ends with this helper.
 */
function withHardeningTail<P extends Processor<any, any, Root, any, any>>(pipeline: P) {
  return pipeline
    .use(rehypeBlockRemoteImages)
    .use(rehypeExternalLinks)
    .use(rehypeStringify)
}

/**
 * Build the full markdown->HTML processor (remark + GFM + rehype + Shiki + link
 * hardening + remote-image blocking) around a Shiki highlighter instance. Takes the
 * highlighter so the main thread can pass its synchronous instance and the worker its
 * own — the rest of the chain (and thus the output) is identical.
 */
export function createMarkdownProcessor(highlighter: HighlighterCore) {
  const base = unified()
    .use(remarkParse)
    .use(remarkGfm)
    .use(remarkLowercaseCodeLang)
    .use(remarkRehype)
    .use(rehypeShikiFromHighlighter, highlighter as Parameters<typeof rehypeShikiFromHighlighter>[0], {
      ...DUAL_THEME_TOKEN_OPTIONS,
      // Fewer token spans: adjacent same-style tokens collapse into one (an
      // upstream merge that is OFF by default), and each remaining span carries
      // a shared style class instead of a ~50-byte inline declaration (see
      // shikiStyleClass — the worker ships the class->declaration dictionary
      // alongside the HTML for main-thread rule injection).
      mergeSameStyleTokens: true,
      transformers: [shikiStyleClassTransformer()],
      // A fence whose language isn't loaded (worker: lazy-load missed it; sync
      // fallback: outside the 20-lang set) or that errors degrades to a plain
      // `text` block instead of throwing the whole document to plain.
      fallbackLanguage: 'text',
      // An unknown/unloaded fence is handled by `fallbackLanguage` WITHOUT reaching
      // here -- `onError` fires only when a LOADED grammar throws at tokenize time
      // (an engine-version mismatch, a grammar that compiles but fails to tokenize,
      // a Safari regex-engine blowup). That's a real regression, so surface it in
      // development; production stays silent (the block already degraded to plain).
      onError: (error) => {
        if (import.meta.env.DEV)
          console.warn('[markdownProcessor] Shiki failed to highlight a code block:', error)
      },
    })
  return withHardeningTail(base)
}

/**
 * Markdown->HTML processor WITHOUT Shiki: the fast synchronous placeholder render
 * (used while the worker's highlighted result is in flight) and the fallback when
 * Shiki throws. Code blocks render as plain `<pre><code class="language-x">` —
 * container-styled but not theme-colored until the highlighted result swaps in.
 */
export const plainMarkdownProcessor = withHardeningTail(
  unified()
    .use(remarkParse)
    .use(remarkGfm)
    .use(remarkRehype),
)

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

// Opening fence: any leading whitespace and blockquote markers, then >=3 backticks
// or tildes, then the info string's first token (the language). Closing fences carry
// no info string so they don't match. The leading `[ \t>]*` (rather than the
// CommonMark-strict "<=3 spaces") is deliberate: remark parses fences nested in
// blockquotes (`> ```py`) and in lists indented past 3 spaces (nested bullets, wide
// ordered markers) as real code nodes with a language, and the worker must pre-load
// those grammars too -- a miss renders that block plain. Over-matching is harmless (an
// extra grammar load at worst); under-matching costs a block its highlight.
const FENCE_LANG_RE = /^[ \t>]*(?:`{3,}|~{3,})[ \t]*([^\s`~]+)/gm

/**
 * Extract the distinct fenced-code-block languages declared in a markdown
 * document, so the worker can lazily load their grammars BEFORE the synchronous
 * `processSync` highlight (which cannot await). Returns raw info-string tokens
 * (lowercased); the caller resolves aliases / unknowns via `ensureLanguage`.
 */
export function extractFenceLanguages(text: string): string[] {
  const langs = new Set<string>()
  for (const match of text.matchAll(FENCE_LANG_RE))
    langs.add(match[1].toLowerCase())
  return [...langs]
}
