// loadExternalScript loads a third-party script by URL, exactly once.
//
// The promise is cached module-wide, so concurrent callers (two captcha
// fields on one page, a retry after a failed render) share one <script>
// insertion and one load result. A failed load is evicted from the cache
// so a later caller can retry; a script that loaded once is assumed to
// stay loaded for the page's lifetime, which is how provider SDKs
// (reCAPTCHA's api.js, Turnstile's api.js) behave anyway.
const loaded = new Map<string, Promise<void>>()

export function loadExternalScript(url: string): Promise<void> {
  const existing = loaded.get(url)
  if (existing)
    return existing
  const promise = new Promise<void>((resolve, reject) => {
    const script = document.createElement('script')
    script.src = url
    script.async = true
    script.onload = () => resolve()
    script.onerror = () => {
      // Evict so the next mount retries instead of replaying the failure.
      loaded.delete(url)
      script.remove()
      reject(new Error(`failed to load script: ${url}`))
    }
    document.head.appendChild(script)
  })
  loaded.set(url, promise)
  return promise
}
