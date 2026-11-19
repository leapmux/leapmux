import { desktopHub, openExternalUrl } from '~/api/platformBridge'
import { safeRedirect } from '~/lib/safeRedirect'

/**
 * Reports whether interactive ceremonies run on the Hub's own pages in the
 * system browser. True only in desktop remote-Hub mode, where the bundled
 * origin cannot complete passkeys, OAuth redirects or captcha.
 */
export function ceremoniesRunInBrowser(): boolean {
  return desktopHub()?.kind === 'remote'
}

/**
 * The origin that serves the Hub's pages. In remote-Hub mode that is the
 * connected Hub's origin; in every other case it is the page origin.
 */
export function hubOrigin(): string {
  const hub = desktopHub()
  if (hub?.kind === 'remote')
    return new URL(hub.url).origin
  return window.location.origin
}

/**
 * Resolves a Hub page path against `hubOrigin()`. Throws when `safeRedirect`
 * rejects the path, so a caller cannot open a cross-origin URL.
 */
export function hubPageUrl(path: string): string {
  const target = safeRedirect(path)
  if (target === undefined)
    throw new Error(`unsafe Hub page path: ${path}`)
  return new URL(target, hubOrigin()).toString()
}

/**
 * Opens a Hub page in the system browser.
 */
export async function openHubPage(path: string): Promise<void> {
  await openExternalUrl(hubPageUrl(path))
}
