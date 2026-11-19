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
  // the start command resolves is captured.
  const earlyResults: BrowserAuthResult[] = []
  let ownCeremonyId: string | null = null
  let resolveResult!: (r: BrowserAuthResult) => void
  let _rejectResult!: (e: Error) => void
  const resultPromise = new Promise<BrowserAuthResult>((resolve, reject) => {
    resolveResult = resolve
    _rejectResult = reject
  })

  const handleEvent = (...args: unknown[]): void => {
    const event = args[0] as { payload: { ceremonyId: string, ok: boolean, failure: string } }
    const p = event.payload
    if (ownCeremonyId !== null && p.ceremonyId !== ownCeremonyId)
      return
    if (ownCeremonyId === null) {
      // Result arrived before the start resolved; buffer it.
      earlyResults.push({ ok: p.ok, failure: p.failure })
      return
    }
    resolveResult({ ok: p.ok, failure: p.failure })
    void platformBridge.onEvent(TAURI_EVENT_BROWSER_AUTH_RESULT, handleEvent).then(off => off())
  }

  const off = await platformBridge.onEvent(TAURI_EVENT_BROWSER_AUTH_RESULT, handleEvent)

  try {
    const start = await platformBridge.startBrowserAuth(kind)
    ownCeremonyId = start.ceremonyId

    // Flush any early result that matched.
    const early = earlyResults[0]
    if (early) {
      resolveResult(early)
    }

    // Check abort before opening the URL.
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

    const cancel = async (): Promise<void> => {
      try {
        await platformBridge.cancelBrowserAuth(start.ceremonyId)
      }
      catch {
        // A rejected cancel settles the result as a cancellation.
        resolveResult({ ok: false, failure: '' })
      }
    }

    // An abort after the start also cancels.
    signal?.addEventListener('abort', () => {
      void cancel()
    }, { once: true })

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
