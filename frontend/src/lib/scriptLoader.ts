// loadExternalScript loads a third-party script by URL, exactly once.
//
// The promise is cached module-wide, so concurrent callers (two captcha
// fields on one page, a retry after a failed render) share one <script>
// insertion and one load result. A failed or timed-out load is evicted
// from the cache so a later caller can retry; a script that loaded once
// is assumed to stay loaded for the page's lifetime, which is how
// provider SDKs (reCAPTCHA's api.js, Turnstile's api.js) behave anyway.
const loaded = new Map<string, Promise<void>>()

// A network path that silently drops packets (a misconfigured firewall, a
// captive portal) fires neither the script's onload nor its onerror.
// The timeout turns that eternal pending state into the caller's error
// path, so a captcha field shows its error state instead of a spinner
// that blocks the submit button forever.
const LOAD_TIMEOUT_MS = 15_000

export function loadExternalScript(url: string): Promise<void> {
  const existing = loaded.get(url)
  if (existing)
    return existing
  const promise = new Promise<void>((resolve, reject) => {
    const script = document.createElement('script')
    let timer: ReturnType<typeof setTimeout> | undefined
    const fail = (err: Error) => {
      clearTimeout(timer)
      // Evict so the next mount retries instead of replaying the failure.
      loaded.delete(url)
      script.remove()
      reject(err)
    }
    timer = setTimeout(() => {
      fail(new Error(`timed out loading script: ${url}`))
    }, LOAD_TIMEOUT_MS)
    script.src = url
    script.async = true
    script.onload = () => {
      clearTimeout(timer)
      resolve()
    }
    script.onerror = () => {
      fail(new Error(`failed to load script: ${url}`))
    }
    document.head.appendChild(script)
  })
  loaded.set(url, promise)
  return promise
}
