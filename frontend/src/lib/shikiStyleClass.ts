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

function injectRule(className: string, decl: string): void {
  // Worker / SSR: no document to inject into. The declarations still travel
  // (via collectShikiStyles) to the main thread, which injects them.
  if (typeof document === 'undefined' || injectedClassNames.has(className))
    return
  injectedClassNames.add(className)
  if (!styleEl) {
    styleEl = document.createElement('style')
    styleEl.setAttribute('data-shiki-style-classes', '')
    document.head.appendChild(styleEl)
  }
  // textContent accumulation re-parses the sheet per addition, but the total
  // rule count is the number of DISTINCT token styles the themes can produce
  // (a few dozen), so the sheet stays tiny and additions stop once the
  // palette is saturated. Unlike insertRule it can't desync from textContent
  // and never throws on a declaration the CSS parser balks at (an unparsable
  // rule is inert, not fatal).
  styleEl.textContent += `.${className}{${decl}}`
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
 * Idempotent per class, so shipping the worker's FULL accumulated dictionary
 * with every response is cheap.
 */
export function ensureShikiStyleRules(styles: Record<string, string>): void {
  for (const [className, decl] of Object.entries(styles))
    injectRule(className, decl)
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
        node.properties.class = node.properties.class
          ? `${String(node.properties.class)} ${className}`
          : className
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
