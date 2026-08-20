import type { SyntaxThemePair } from './syntaxThemes'
import { createMarkdownProcessor, extractFenceLanguages, plainMarkdownProcessor, renderWithPlainFallback } from './markdownProcessor'
import { createLazyOnigurumaHighlighter } from './shikiLazyHighlighter'
import { collectNewShikiStyles } from './shikiStyleClass'
import { createPairKeyedCache, setSyntaxThemePair } from './shikiThemes'

// ---------------------------------------------------------------------------
// Markdown render worker
//
// Runs the FULL remark+rehype+Shiki pipeline (the expensive part is Shiki's
// synchronous tokenization, which blocked the main thread for up to ~1s on large
// code-heavy messages). Off the main thread it can take as long as it needs
// without dropping a frame. The output matches the main thread's synchronous
// fallback structurally (same processor config + themes -- see markdownProcessor),
// so the result drops straight into the shared markdown cache and the CSS that
// themes Shiki's `pre.shiki` structure works unchanged.
//
// This worker uses the Oniguruma WASM engine and loads grammars lazily. Because
// `processSync` cannot await, every fenced-code language is pre-loaded BEFORE the
// render; an unknown/unloaded fence degrades to a plain block via the processor's
// `fallbackLanguage`. The async part is the one-time highlighter init plus those
// lazy grammar loads.
// ---------------------------------------------------------------------------

export interface MarkdownRenderRequest {
  type: 'render'
  id: number
  text: string
  /** The syntax theme pair to render under; see TokenizeRequest for why per-request. */
  syntax: SyntaxThemePair
}

export interface MarkdownRenderResponse {
  type: 'render-result'
  id: number
  html: string
  /**
   * True when this render degraded because a real grammar FAILED to load transiently
   * (a chunk-import hiccup) or the highlighter init threw -- NOT because a fence used a
   * language Shiki doesn't bundle. The client must not cache a retryable render as
   * permanent; a later re-dispatch may highlight it once the load recovers.
   */
  retryable: boolean
  /**
   * className -> CSS declaration for each shared token-style class this worker
   * has minted SINCE ITS LAST RESPONSE (see collectNewShikiStyles). The worker
   * has no document to inject rules into, so the main thread must
   * (ensureShikiStyleRules) before the HTML renders.
   *
   * A DELTA, not a snapshot, and it is NOT limited to the classes in `html`:
   * a response carries whatever this worker recorded since the last one, which
   * is a superset of what its own HTML refers to and a subset of what it has
   * ever minted. Usually it is empty, because the declarations of a theme
   * saturate after the first few code blocks.
   *
   * The full record used to ship on every response, and `declByClassName` is
   * never pruned. That was cheap while the syntax theme was fixed, because the
   * map then held one entry per distinct token style of ONE pair. The theme
   * picker changed the premise: a session that samples several of the thirty
   * variants accumulates each one's distinct declarations in the same map, and
   * every later render cloned the whole of it across `postMessage`.
   */
  styles: Record<string, string>
}

const hl = createLazyOnigurumaHighlighter()

/**
 * The processor for `pair`, rebuilt when the pair moves.
 *
 * Keyed because `createMarkdownProcessor` bakes the pair in: an unkeyed cache
 * kept tokenizing with whatever theme was live on this worker's FIRST message,
 * for the rest of the page's life. See `createPairKeyedCache`, which also says
 * why this is called at the point of use -- this handler suspends twice
 * between a request arriving and its render.
 */
const processorFor = createPairKeyedCache<ReturnType<typeof createMarkdownProcessor>>()

globalThis.onmessage = async (e: MessageEvent<MarkdownRenderRequest>) => {
  const msg = e.data
  if (msg.type !== 'render')
    return
  let html: string
  // Whether this render degraded due to a transient failure (retry may recover) rather
  // than a permanent one (unknown language / genuinely un-highlightable), so the client
  // knows not to cache it forever.
  let retryable = false
  try {
    // ensureReady() is INSIDE the try: an init rejection must still answer this
    // request (with a plain render below), or the client's pending promise + the
    // main thread's inFlight entry for this text would hang forever (mirrors
    // shikiWorker). The factory drops the cached init promise on rejection so a
    // later message retries instead of re-awaiting the same failure forever.
    // Adopt the request's pair BEFORE `ensureReady`, not after. The lazy
    // highlighter's first-time init registers whatever pair this isolate
    // currently names, so setting it afterwards made every fresh worker fetch
    // and parse the DEFAULT pair's two TextMate documents that it would then
    // never tokenize with, and await that import before the first body could
    // render. `~/lib/shikiWorker` adopts the pair in the same order and for
    // the same reason.
    //
    // That init registration is the ONLY reader of the module state in this
    // isolate: `collectShikiStyles` keys on the declaration text and the plain
    // fallback runs no Shiki, so neither reads the pair. What decides this
    // request's COLOURS is the pair passed to `processorFor` below.
    setSyntaxThemePair(msg.syntax)
    const highlighter = await hl.ensureReady()
    await hl.ensureThemes(msg.syntax)

    // Lazily load each fenced language before the synchronous highlight. A 'failed'
    // result is a real grammar whose chunk load threw transiently -- that fence rendered
    // plain and the whole render should be retried, not cached. An 'unsupported' fence
    // (no bundled grammar) is correctly, permanently plain and is NOT retryable.
    const langs = extractFenceLanguages(msg.text)
    if (langs.length > 0) {
      const results = await Promise.all(langs.map(lang => hl.ensureLanguage(lang)))
      retryable = results.includes('failed')
    }
    html = renderWithPlainFallback(processorFor(msg.syntax, () => createMarkdownProcessor(highlighter, msg.syntax)), msg.text)
  }
  catch {
    // Highlighter init failed -- the plain processor needs no highlighter, so the
    // body still renders (un-highlighted) rather than stranding the request. The
    // failure is transient (the factory drops the cached init promise on rejection),
    // so mark it retryable instead of letting the client cache plain permanently.
    html = String(plainMarkdownProcessor.processSync(msg.text))
    retryable = true
  }
  const response: MarkdownRenderResponse = { type: 'render-result', id: msg.id, html, retryable, styles: collectNewShikiStyles() }
  globalThis.postMessage(response)
}
