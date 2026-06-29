import { createMarkdownProcessor, extractFenceLanguages, plainMarkdownProcessor, renderWithPlainFallback } from './markdownProcessor'
import { createLazyOnigurumaHighlighter } from './shikiLazyHighlighter'

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
}

const hl = createLazyOnigurumaHighlighter()
let processor: ReturnType<typeof createMarkdownProcessor> | null = null

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
    const highlighter = await hl.ensureReady()
    if (!processor)
      processor = createMarkdownProcessor(highlighter)
    // Lazily load each fenced language before the synchronous highlight. A 'failed'
    // result is a real grammar whose chunk load threw transiently -- that fence rendered
    // plain and the whole render should be retried, not cached. An 'unsupported' fence
    // (no bundled grammar) is correctly, permanently plain and is NOT retryable.
    const langs = extractFenceLanguages(msg.text)
    if (langs.length > 0) {
      const results = await Promise.all(langs.map(lang => hl.ensureLanguage(lang)))
      retryable = results.includes('failed')
    }
    html = renderWithPlainFallback(processor, msg.text)
  }
  catch {
    // Highlighter init failed -- the plain processor needs no highlighter, so the
    // body still renders (un-highlighted) rather than stranding the request. The
    // failure is transient (the factory drops the cached init promise on rejection),
    // so mark it retryable instead of letting the client cache plain permanently.
    html = String(plainMarkdownProcessor.processSync(msg.text))
    retryable = true
  }
  const response: MarkdownRenderResponse = { type: 'render-result', id: msg.id, html, retryable }
  globalThis.postMessage(response)
}
