import { createHighlighterCore } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { createMarkdownProcessor, plainMarkdownProcessor, renderWithPlainFallback, shikiLangs, transparentBgThemes } from './markdownProcessor'

// ---------------------------------------------------------------------------
// Markdown render worker
//
// Runs the FULL remark+rehype+Shiki pipeline (the expensive part is Shiki's
// synchronous tokenization, which blocked the main thread for up to ~1s on large
// code-heavy messages). Off the main thread it can take as long as it needs
// without dropping a frame. The output is byte-identical to the main thread's
// synchronous path (same processor config, themes, langs -- see markdownProcessor),
// so the result drops straight into the shared markdown cache and the CSS that
// themes Shiki's `pre.shiki` structure works unchanged.
//
// `processSync` is fine HERE: it is synchronous within the worker, not on the UI
// thread. The async part is only the one-time highlighter init (lang loading).
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
}

let processor: ReturnType<typeof createMarkdownProcessor> | null = null
let initPromise: Promise<void> | null = null

async function ensureProcessor(): Promise<void> {
  if (processor)
    return
  if (!initPromise) {
    initPromise = createHighlighterCore({
      themes: transparentBgThemes,
      langs: shikiLangs,
      engine: createJavaScriptRegexEngine(),
    }).then((h) => { processor = createMarkdownProcessor(h) })
    // If init REJECTS (a grammar/engine load failure), clear the cached promise so a
    // later message retries init instead of re-awaiting the same rejection forever --
    // otherwise one transient init failure would downgrade every body to plain for the
    // worker's whole lifetime.
    void initPromise.catch(() => {
      initPromise = null
    })
  }
  await initPromise
}

globalThis.onmessage = async (e: MessageEvent<MarkdownRenderRequest>) => {
  const msg = e.data
  if (msg.type !== 'render')
    return
  let html: string
  try {
    // ensureProcessor() is INSIDE the try: an init rejection must still answer this
    // request (with a plain render below), or the client's pending promise + the main
    // thread's inFlight entry for this text would hang forever (mirrors shikiWorker).
    await ensureProcessor()
    html = renderWithPlainFallback(processor!, msg.text)
  }
  catch {
    // Highlighter init failed -- the plain processor needs no highlighter, so the body
    // still renders (un-highlighted) rather than stranding the request.
    html = String(plainMarkdownProcessor.processSync(msg.text))
  }
  const response: MarkdownRenderResponse = { type: 'render-result', id: msg.id, html }
  globalThis.postMessage(response)
}
