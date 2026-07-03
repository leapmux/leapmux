import type { ShikiTransformer } from 'shiki/core'
import { fnv1a32Hex } from './stringDigest'

// ---------------------------------------------------------------------------
// Shared classes for Shiki token styles
//
// A large highlighted body repeats a handful of distinct dual-theme style
// declarations (`--shiki-light:#24292E;--shiki-dark:#E1E4E8`, ...) across
// thousands of token <span>s. Stamping each span with an inline `style`
// costs ~50 bytes of markup per token AND gives every span a private inline
// CSSStyleDeclaration, which defeats the browser's computed-style sharing —
// restyling a 10k-span diff computes 10k unique styles instead of reusing
// the ~30 distinct ones.
//
// This module mints ONE class per distinct declaration instead. The class
// name is derived purely from the declaration text (hash + length, mirroring
// renderArtifactStore's artifactKey collision guard), so any thread — the
// markdown worker, the token worker's client, the main-thread ANSI path —
// derives the same name for the same declaration without coordination. The
// declarations themselves reach the main thread either directly (the code
// runs here) or as a small {className: declaration} dictionary shipped
// alongside worker-rendered HTML, and `ensureShikiStyleRules` injects each
// rule once into a dedicated <style> element.
//
// The rules only DEFINE the `--shiki-*` variables; the consuming CSS
// (shikiDualThemeColors selectors) is unchanged.
// ---------------------------------------------------------------------------

/**
 * Canonical declaration text for a token's htmlStyle: the exact form
 * `tokensToHast` serializes into a style attribute (`key:value;key:value`),
 * so the token path and the HTML-transformer path mint identical classes for
 * identical styles. Returns '' for an unstyled token (no class needed).
 */
export function shikiStyleDecl(style: string | Record<string, string>): string {
  if (typeof style === 'string')
    return style
  return Object.entries(style).map(([key, value]) => `${key}:${value}`).join(';')
}

/**
 * The deterministic class name for a declaration. The length term makes a
 * 32-bit digest collision across DISTINCT declarations require an equal
 * length too (the artifactKey pattern) — and the residual worst case is a
 * wrong token color, never breakage.
 */
export function shikiStyleClassName(decl: string): string {
  return `sk-${fnv1a32Hex(decl)}-${decl.length.toString(36)}`
}

/** className -> declaration, everything recorded in THIS thread. */
const declByClassName = new Map<string, string>()
/** Class names whose CSS rule has been injected into this thread's document. */
const injectedClassNames = new Set<string>()
let styleEl: HTMLStyleElement | null = null

/**
 * The dedicated <style> element for this thread's shared token-style rules, created lazily
 * on first use and appended to <head> so its live CSSOM sheet is available for insertRule.
 * Returns null when there is no document (worker / SSR) -- there the declarations still
 * travel (via collectShikiStyles) to the main thread, which injects them.
 */
function ensureStyleEl(): HTMLStyleElement | null {
  if (typeof document === 'undefined')
    return null
  if (!styleEl) {
    styleEl = document.createElement('style')
    styleEl.setAttribute('data-shiki-style-classes', '')
    document.head.appendChild(styleEl)
  }
  return styleEl
}

/**
 * Append ONE `.class{decl}` rule via CSSOM insertRule -- an O(1) append that does NOT
 * re-parse the accumulated sheet. A `textContent +=` per rule re-parsed the WHOLE sheet, so
 * injecting N distinct declarations one at a time was quadratic in N: cheap for the dual-theme
 * SYNTAX palette (a few dozen, saturating quickly) but NOT for ANSI/truecolor tool output,
 * which can mint hundreds-to-thousands of distinct fg/bg/decoration declarations over a
 * session. insertRule throws SyntaxError on a declaration the CSS parser rejects, so the
 * insert is guarded: an unparsable rule is dropped and left inert, exactly as the browser
 * dropped an invalid rule under the old textContent write. No-op when the element exposes no
 * live sheet (defensive; a <style> connected to the document always does).
 */
function insertStyleRule(el: HTMLStyleElement, className: string, decl: string): void {
  const sheet = el.sheet
  if (!sheet)
    return
  try {
    sheet.insertRule(`.${className}{${decl}}`, sheet.cssRules.length)
  }
  catch {
    // Unparsable declaration -- inert, skip (matches the browser dropping an invalid rule).
  }
}

function injectRule(className: string, decl: string): void {
  if (injectedClassNames.has(className))
    return
  // Worker / SSR: no document to inject into (ensureStyleEl returns null). The declarations
  // still travel (via collectShikiStyles) to the main thread, which injects them.
  const el = ensureStyleEl()
  if (!el)
    return
  injectedClassNames.add(className)
  insertStyleRule(el, className, decl)
}

/**
 * Record a declaration and return its class name, injecting the CSS rule when
 * a document is available. Idempotent; '' declarations record nothing and
 * return undefined (an unstyled token gets no class).
 */
export function recordShikiStyle(decl: string): string | undefined {
  if (decl === '')
    return undefined
  const className = shikiStyleClassName(decl)
  const existing = declByClassName.get(className)
  if (existing === undefined) {
    declByClassName.set(className, decl)
    injectRule(className, decl)
  }
  else if (import.meta.env.DEV && existing !== decl) {
    console.warn(`[shikiStyleClass] class collision: ${className} maps to both "${existing}" and "${decl}"`)
  }
  return className
}

/**
 * Inject rules for a worker-shipped {className: declaration} dictionary.
 * Idempotent per class. Each not-yet-injected rule is appended via insertRule
 * (see insertStyleRule) -- an O(1) CSSOM append, so injecting a response's whole
 * dictionary is linear in the number of NEW classes with no full-sheet re-parse.
 */
export function ensureShikiStyleRules(styles: Record<string, string>): void {
  const el = ensureStyleEl()
  if (!el)
    return
  for (const [className, decl] of Object.entries(styles)) {
    if (injectedClassNames.has(className))
      continue
    injectedClassNames.add(className)
    insertStyleRule(el, className, decl)
  }
}

/**
 * Snapshot of every declaration recorded in this thread, keyed by class name.
 * The markdown worker ships this alongside rendered HTML so the main thread
 * can inject the rules its class names refer to.
 */
export function collectShikiStyles(): Record<string, string> {
  return Object.fromEntries(declByClassName)
}

/**
 * Shiki transformer that moves each token span's inline style declaration
 * into a shared class (recording it for `collectShikiStyles`). Applied by
 * every `codeToHast`/`codeToHtml` call site (the markdown pipeline and the
 * ANSI renderer); the token (`codeToTokens`) paths convert at their own
 * boundaries instead (see toCachedTokens / expandInternedTokenLines).
 *
 * Only token spans are touched: the `pre.shiki` root keeps its per-block
 * rootStyle (one element), and unstyled spans stay attribute-free.
 */
export function shikiStyleClassTransformer(): ShikiTransformer {
  return {
    name: 'leapmux:style-class',
    span(node) {
      const style = node.properties?.style
      if (typeof style !== 'string' || style === '')
        return node
      const className = recordShikiStyle(style)
      if (className !== undefined) {
        delete node.properties.style
        // hast permits `class` as a string OR a string[] -- append into an array in
        // array form (String([...]) would comma-join the existing classes into one
        // invalid token), and concatenate a string form.
        const existing = node.properties.class
        if (existing === undefined || existing === '')
          node.properties.class = className
        else if (Array.isArray(existing))
          node.properties.class = [...existing, className]
        else
          node.properties.class = `${String(existing)} ${className}`
      }
      return node
    },
  }
}

/** Visible for testing: forget every recorded declaration and injected rule. */
export function _resetShikiStyleClassesForTest(): void {
  declByClassName.clear()
  injectedClassNames.clear()
  styleEl?.remove()
  styleEl = null
}
