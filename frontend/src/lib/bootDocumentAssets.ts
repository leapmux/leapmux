/**
 * Cold-start document assets: splash paints first; app CSS and deep JS
 * preloads wait until after that paint.
 *
 * Solid Start's SPA `StartServer` already maps `context.assets` through
 * `renderAsset` into the `assets` prop. This module reads the RAW list from
 * `getRequestEvent().assets` and re-renders a filtered set:
 *
 * - stylesheets: `media="print"` + `data-boot-defer-css`, promoted to
 *   `media="all"` after a double rAF (see `bootDeferStylesScript`)
 * - modulepreload: keep only the client entry chunk; drop AuthContext,
 *   captcha, artifact stores, and the rest until the entry graph imports them
 *
 * The promote script is a hashed inline `<script>` (CSP-safe). Do not use
 * `onload=` on the links — that needs `'unsafe-hashes'`.
 */

/** Marker on deferred stylesheet links; the promote script selects on it. */
export const BOOT_DEFER_CSS_ATTR = 'data-boot-defer-css'

export interface BootDocumentAsset {
  tag: string
  attrs?: Record<string, unknown>
  children?: unknown
}

function hrefOf(asset: BootDocumentAsset): string {
  const href = asset.attrs?.href
  return typeof href === 'string' ? href : ''
}

function isStylesheet(asset: BootDocumentAsset): boolean {
  return asset.tag === 'link' && asset.attrs?.rel === 'stylesheet'
}

function isModulepreload(asset: BootDocumentAsset): boolean {
  return asset.tag === 'link' && asset.attrs?.rel === 'modulepreload'
}

/** Last path segment of a href, ignoring a leading `./` and a query string. */
function hrefBasename(href: string): string {
  const path = href.replace(/^\.\//, '/').split('?', 1)[0] ?? href
  const slash = path.lastIndexOf('/')
  return slash >= 0 ? path.slice(slash + 1) : path
}

/**
 * True when this modulepreload is the SPA client entry (not a deep chunk).
 *
 * Prefer an exact match against the handler output path from the client
 * manifest. Fall back to equal basenames, then to the vinxi
 * `client-<hash>.js` basename when the manifest is absent (unit tests).
 */
export function isClientEntryModulepreload(
  href: string,
  clientEntryHref?: string,
): boolean {
  if (!href)
    return false
  if (clientEntryHref) {
    const a = href.replace(/^\.\//, '/')
    const b = clientEntryHref.replace(/^\.\//, '/')
    if (a === b || hrefBasename(a) === hrefBasename(b))
      return true
    return false
  }
  return /\/client-[^/]+\.js(?:\?.*)?$/.test(href)
}

/**
 * Client entry href from Vinxi's build manifest, when the document runs
 * under StartServer. Undefined in unit tests.
 */
export function resolveClientEntryHref(): string | undefined {
  const manifest = (import.meta.env as { MANIFEST?: { client?: {
    handler?: string
    inputs?: Record<string, { output?: { path?: string } }>
  } } }).MANIFEST?.client
  if (!manifest?.handler || !manifest.inputs)
    return undefined
  const path = manifest.inputs[manifest.handler]?.output?.path
  return typeof path === 'string' ? path : undefined
}

export function partitionBootDocumentAssets(
  assets: readonly BootDocumentAsset[],
  clientEntryHref?: string,
): {
  immediate: BootDocumentAsset[]
  deferredStylesheets: BootDocumentAsset[]
} {
  const immediate: BootDocumentAsset[] = []
  const deferredStylesheets: BootDocumentAsset[] = []

  for (const asset of assets) {
    if (isStylesheet(asset)) {
      deferredStylesheets.push(asset)
      continue
    }
    if (isModulepreload(asset)) {
      if (isClientEntryModulepreload(hrefOf(asset), clientEntryHref))
        immediate.push(asset)
      continue
    }
    immediate.push(asset)
  }

  return { immediate, deferredStylesheets }
}

/**
 * Attrs for a stylesheet that must not block first paint of the splash.
 * Drops `fetchPriority` so the browser does not treat the file as critical
 * while the splash CSS (inline) already owns the first frame.
 */
export function deferredStylesheetAttrs(
  attrs: Record<string, unknown>,
): Record<string, unknown> {
  const next: Record<string, unknown> = { ...attrs }
  delete next.key
  delete next.fetchPriority
  next.rel = 'stylesheet'
  next.media = 'print'
  next[BOOT_DEFER_CSS_ATTR] = ''
  return next
}

/**
 * Promote deferred stylesheets after the first paint opportunity.
 * Double rAF: the first callback is before the next paint; the second runs
 * after it, so the splash frame can commit with only the inline splash CSS.
 * Also promotes on `visibilitychange` so a background-tab load that deferred
 * rAF still gets app CSS when the tab becomes visible.
 */
export function bootDeferStylesScript(): string {
  const attr = BOOT_DEFER_CSS_ATTR
  const attrLit = JSON.stringify(attr)
  return `(function(){function p(){var n=document.querySelectorAll("link[${attr}]");for(var i=0;i<n.length;i++){n[i].media="all";n[i].removeAttribute(${attrLit});}}function s(){if(typeof requestAnimationFrame==="function"){requestAnimationFrame(function(){requestAnimationFrame(p);});}else{p();}}s();document.addEventListener("visibilitychange",function(){if(document.visibilityState==="visible")p();});})();`
}
