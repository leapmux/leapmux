import type { BrowserAuthKind } from '~/api/platformBridge'
import { openExternalUrl, platformBridge } from '~/api/platformBridge'
import { TAURI_EVENT_BROWSER_AUTH_RESULT } from '~/generated/contracts/desktop'

export interface BrowserAuthResult {
  ok: boolean
  failure: string
}

export interface BrowserAuthFlow {
  url: string
  result: Promise<BrowserAuthResult>
  cancel: () => Promise<void>
}

/**
 * Runs a browser ceremony for the desktop's session. The sidecar emits
 * exactly one result event per ceremony; this function settles only on the
 * event that carries its own ceremony id.
 */
export async function runBrowserAuth(
  kind: BrowserAuthKind,
  signal?: AbortSignal,
): Promise<BrowserAuthFlow> {
  // Register the listener BEFORE starting, so a result that arrives before
  // the start command resolves is captured. The payload is kept whole: the
  // ceremony id decides at flush whether an entry is this call's or a stale
  // result of a superseded ceremony.
  const earlyResults: { ceremonyId: string, ok: boolean, failure: string }[] = []
  let ownCeremonyId: string | null = null
  let resolveResult!: (r: BrowserAuthResult) => void
  const resultPromise = new Promise<BrowserAuthResult>((resolve) => {
    resolveResult = resolve
  })

  // `onEvent` hands the callback the emitted payload -- the fields of
  // `BrowserAuthResultEvent`, as the shell forwards them. The Tauri event
  // envelope is unwrapped upstream, so `args[0]` IS the result here.
  const handleEvent = (...args: unknown[]): void => {
    const p = args[0] as { ceremonyId: string, ok: boolean, failure: string }
    if (ownCeremonyId !== null && p.ceremonyId !== ownCeremonyId)
      return
    if (ownCeremonyId === null) {
      // Result arrived before the start resolved; buffer it.
      earlyResults.push({ ceremonyId: p.ceremonyId, ok: p.ok, failure: p.failure })
      return
    }
    resolveResult({ ok: p.ok, failure: p.failure })
  }

  const off = await platformBridge.onEvent(TAURI_EVENT_BROWSER_AUTH_RESULT, handleEvent)

  try {
    const start = await platformBridge.startBrowserAuth(kind)
    ownCeremonyId = start.ceremonyId

    // Flush the early result of this ceremony. A buffered result that
    // carries another ceremony id is stale and must not settle this call.
    const early = earlyResults.find(r => r.ceremonyId === start.ceremonyId)
    if (early)
      resolveResult({ ok: early.ok, failure: early.failure })

    const cancel = async (): Promise<void> => {
      try {
        await platformBridge.cancelBrowserAuth(start.ceremonyId)
      }
      catch {
        // A rejected cancel settles the result as a cancellation.
        resolveResult({ ok: false, failure: '' })
      }
    }

    // An abort after the start also cancels. Register before opening the
    // URL, so an abort during the open is not lost.
    signal?.addEventListener('abort', () => {
      void cancel()
    }, { once: true })

    // An already-aborted signal never fires its listener. Run the abort
    // path at once instead of opening the URL.
    if (signal?.aborted) {
      await platformBridge.cancelBrowserAuth(start.ceremonyId)
      resolveResult({ ok: false, failure: '' })
      off()
      return {
        url: start.authorizeUrl,
        result: resultPromise,
        cancel: () => Promise.resolve(),
      }
    }

    await openExternalUrl(start.authorizeUrl)

    // Remove the listener when the result settles.
    void resultPromise.then(() => off())

    return {
      url: start.authorizeUrl,
      result: resultPromise,
      cancel,
    }
  }
  catch (err) {
    off()
    throw err
  }
}
