import type { Root } from 'hast'
import type { Root as MdastRoot } from 'mdast'
import type { HighlighterCore } from 'shiki/core'
import type { Processor } from 'unified'
import type { SyntaxThemePair } from './syntaxThemes'
import rehypeShikiFromHighlighter from '@shikijs/rehype/core'
import rehypeStringify from 'rehype-stringify'
import remarkRehype from 'remark-rehype'
import { visit } from 'unist-util-visit'
import { createMarkdownParser } from './markdownParse'
import { rehypeBlockRemoteImages } from './rehypeBlockRemoteImages'
import { shikiStyleClassTransformer } from './shikiStyleClass'
import { dualThemeTokenOptions } from './shikiThemes'

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
        // No `properties ??= {}` guard: reaching here means `properties.href`
        // already yielded a string, so `properties` is necessarily present.
        node.properties.target = '_blank'
        // `rel` is a space-separated token list, which hast models as an array;
        // hast-util-to-html joins it back into `rel="noopener noreferrer nofollow"`.
        node.properties.rel = ['noopener', 'noreferrer', 'nofollow']
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
 *
 * `pair` IS BAKED IN, so a processor belongs to exactly one syntax theme and a
 * caller that keeps one must rebuild it when the theme changes.
 * `rehypeShikiFromHighlighter` destructures its options ONCE when the plugin
 * attaches and reuses that frozen object for every `highlight()` call, so
 * spreading the live module pair here pinned every processor to whichever theme
 * happened to be current when it was built — the exact capture that
 * `dualThemeTokenOptions` is a function to prevent. Taking the pair as an
 * argument makes the dependency visible at the call site instead of hiding it
 * in module state.
 */
export function createMarkdownProcessor(highlighter: HighlighterCore, pair: SyntaxThemePair) {
  const base = createMarkdownParser()
    .use(remarkLowercaseCodeLang)
    .use(remarkRehype)
    .use(rehypeShikiFromHighlighter, highlighter as Parameters<typeof rehypeShikiFromHighlighter>[0], {
      ...dualThemeTokenOptions(pair),
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
  createMarkdownParser()
    .use(remarkRehype),
)

/**
 * Render `text` through `processor` (highlighted), degrading to the plain
 * (un-highlighted) processor when the highlighted pipeline throws. Single-sources
 * the "on failure, fall back to plain" rule that BOTH the main-thread synchronous
 * path and the worker apply, so the two can't drift on what counts as a fallback.
 *
 * This is the WIDER net of the two error paths, not the narrower one. A grammar
 * that throws at tokenize time -- the Safari regex-engine case -- is raised
 * inside `codeToHast` and caught by `onError` above, which leaves the rest of the
 * document highlighted. What reaches here is everything else: a disposed or
 * mismatched highlighter, `getLoadedLanguages()` failing before the transformer's
 * own try, or any remark/rehype plugin in the chain. Those take the WHOLE
 * document down to plain, so they warn at least as loudly as the per-block case.
 */
export function renderWithPlainFallback(
  processor: ReturnType<typeof createMarkdownProcessor>,
  text: string,
): string {
  try {
    return String(processor.processSync(text))
  }
  catch (error) {
    if (import.meta.env.DEV)
      console.warn('[markdownProcessor] highlighted render failed; falling back to plain:', error)
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
