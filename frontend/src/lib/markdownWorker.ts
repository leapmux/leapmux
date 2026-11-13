import type { SyntaxThemePair } from './syntaxThemes'
import { createMarkdownProcessor, extractFenceLanguages, renderPlainOrSource, renderWithPlainFallback } from './markdownProcessor'
import { createLazyOnigurumaHighlighter } from './shikiLazyHighlighter'
import { collectNewShikiStyles } from './shikiStyleClass'
import { createPairKeyedCache, setSyntaxThemePair } from './shikiThemes'

// ---------------------------------------------------------------------------
// Markdown render worker
//
// Runs the FULL remark+rehype+Shiki pipeline (the expensive part is Shiki's
// synchronous tokenization, which blocked the main thread for up to ~1s on large
// code-heavy messages). Off the main thread it can take as long as it needs
// without dropping a frame. The main thread never parses Markdown: the result
// drops straight into the shared markdown cache (see renderMarkdown).
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
   *
   * Always false when the parser failed and `html` shows the escaped source: every
   * processor shares that parser, so a re-dispatch would fail the same way.
   */
  retryable: boolean
  /**
   * className -> CSS declaration for each shared token-style class this worker
   * has minted SINCE ITS LAST RESPONSE (see collectNewShikiStyles). The worker
   * has no document to inject rules into, so the main thread must
   * (ensureShikiStyleRules) before the HTML renders, and also when it discards
   * the HTML, because no later response carries these declarations again.
   *
   * A DELTA, not the dictionary of `html`. `html` can use a class that an
   * earlier render of this worker minted, and this omits it. It can also hold a
   * class that `html` does not use, from a render that fell back to plain
   * Markdown after Shiki minted it. Usually it is empty, because the
   * declarations of a theme saturate after the first few code blocks. A caller
   * that needs the dictionary of `html` builds it on the main thread (see
   * shikiStylesUsedBy).
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
 * why this is called at the point of use -- `answerMarkdownRequest` suspends
 * several times between a request arriving and its render.
 */
const processorFor = createPairKeyedCache<ReturnType<typeof createMarkdownProcessor>>()

/**
 * Render one request and return its reply. Every render failure still answers.
 *
 * It does not adopt the request's pair as this isolate's pair: the message
 * handler below does that first (see there). The unit tests run this function on
 * the main thread through a stand-in Worker, where that step would change the
 * pair of the page.
 */
export async function answerMarkdownRequest(msg: MarkdownRenderRequest): Promise<MarkdownRenderResponse> {
  let html: string
  // Whether this render degraded due to a transient failure (retry may recover) rather
  // than a permanent one (unknown language / genuinely un-highlightable), so the client
  // knows not to cache it forever.
  let retryable = false
  try {
    // ensureReady() is INSIDE the try: an init rejection must still answer this
    // request (with a plain render below), as in shikiWorker. A request with no
    // reply keeps its row a skeleton until the client's time budget for it runs out
    // (see markdownRenderBudgetMs). The client then terminates this worker, posts
    // the requests behind it again to a fresh worker, and resolves this one to
    // null, so its row shows the escaped source. The factory drops the cached init
    // promise on rejection, so a later message retries instead of re-awaiting the
    // same failure forever.
    //
    // What decides this request's COLOURS is the pair passed to `ensureThemes`
    // and `processorFor` below, never the module state of this isolate.
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
    const output = renderWithPlainFallback(processorFor(msg.syntax, () => createMarkdownProcessor(highlighter, msg.syntax)), msg.text)
    html = output.html
    // A body that the parser cannot parse shows its source, and a retry after the
    // grammar loads fails the same way.
    if (!output.parsed)
      retryable = false
  }
  catch {
    // Highlighter init failed -- the plain processor needs no highlighter, so the
    // body still renders (un-highlighted) rather than stranding the request. The
    // failure is transient (the factory drops the cached init promise on rejection),
    // so mark a plain render retryable instead of letting the client cache plain
    // permanently. A body that shows its source because the parser failed is not
    // retryable: the highlighted processor shares that parser.
    //
    // `renderPlainOrSource`, not the plain processor directly: a parse that throws
    // HERE rejects this function's promise, and `Worker.onerror` does not report a
    // rejection. The request then gets no reply (see the comment on `ensureReady`).
    const output = renderPlainOrSource(msg.text)
    html = output.html
    retryable = output.parsed
  }
  return {
    type: 'render-result',
    id: msg.id,
    html,
    retryable,
    styles: collectNewShikiStyles(),
  }
}

/**
 * Render one request and post its reply.
 *
 * Adopt the request's pair BEFORE the render's `ensureReady`, not after. The lazy
 * highlighter's first-time init registers whatever pair this isolate currently
 * names, so setting it afterwards made every fresh worker fetch and parse the
 * DEFAULT pair's two TextMate documents that it would then never tokenize with,
 * and await that import before the first body could render. `~/lib/shikiWorker`
 * adopts the pair in the same order and for the same reason.
 *
 * That init registration is the ONLY reader of the module state in this isolate:
 * `collectShikiStyles` keys on the declaration text and the plain fallback runs no
 * Shiki, so neither reads the pair.
 */
async function renderAndReply(msg: MarkdownRenderRequest): Promise<void> {
  setSyntaxThemePair(msg.syntax)
  globalThis.postMessage(await answerMarkdownRequest(msg))
}

/**
 * Settles once the request that arrived last is finished, whether its reply was
 * posted or not. Each request starts only then, so this worker renders one
 * request at a time and answers in arrival order.
 *
 * The client depends on that order. It charges its time budget to the request at
 * the front of its queue (see `createWorkerClient`), so a request must not wait
 * for a grammar load while a later one parses. If it did, the later parse would
 * use up the budget of the earlier request, and the client would fail the earlier
 * request in its place.
 */
let previousReply: Promise<void> = Promise.resolve()

globalThis.onmessage = (e: MessageEvent<MarkdownRenderRequest>): Promise<void> | undefined => {
  const msg = e.data
  if (msg.type !== 'render')
    return undefined
  const reply = previousReply.then(() => renderAndReply(msg))
  // A rejection leaves this request without a reply, but must not stop the
  // requests behind it.
  previousReply = reply.catch((error: unknown) => {
    console.error('[markdownWorker] a render failed before its reply was posted:', error)
  })
  return reply
}
