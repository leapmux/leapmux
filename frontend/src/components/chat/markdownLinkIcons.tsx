import type { LucideIcon } from 'lucide-solid'
import type { LocalLinkKind } from '~/lib/localLinks'
import FileIcon from 'lucide-solid/icons/file'
import FileImageIcon from 'lucide-solid/icons/file-image'
import FolderClosed from 'lucide-solid/icons/folder-closed'
import Globe from 'lucide-solid/icons/globe'
import { render } from 'solid-js/web'
import { isMarkdownImageElement } from '~/lib/rehypeBlockRemoteImages'
import { markdownLinkIconClass } from './markdownEditor/markdownContent.css'

export type MarkdownLinkIconKind = LocalLinkKind | 'remote'

/** Data attribute recording the classified kind on each prepended link icon SVG. */
export const MARKDOWN_LINK_ICON_ATTRIBUTE = 'data-markdown-link-icon'

const ICON_COMPONENTS: Record<MarkdownLinkIconKind, LucideIcon> = {
  file: FileIcon,
  directory: FolderClosed,
  image: FileImageIcon,
  remote: Globe,
}

const prototypeCache = new Map<MarkdownLinkIconKind, SVGSVGElement>()

/** Render each Lucide icon once into a detached DOM node and cache a cloned prototype. */
function getIconPrototype(kind: MarkdownLinkIconKind): SVGSVGElement {
  const cached = prototypeCache.get(kind)
  if (cached)
    return cached

  const IconComponent = ICON_COMPONENTS[kind]
  const host = document.createElement('span')
  let prototype: SVGSVGElement | undefined
  // Pass `{ owner: null }` so rendering inside a reactive effect never inherits
  // an ancestor context (such as a custom LucideProvider) into the shared cache.
  const dispose = render(() => <IconComponent />, host, undefined, { owner: null })
  try {
    const svg = host.querySelector('svg')
    if (!svg)
      throw new Error(`Expected Lucide icon "${kind}" to render an <svg> element`)
    // Clone before dispose(), because Solid's render disposer clears the host's DOM children.
    prototype = svg.cloneNode(true) as SVGSVGElement
  }
  finally {
    dispose()
  }

  prototype.classList.add(markdownLinkIconClass)
  prototype.setAttribute('aria-hidden', 'true')
  prototype.setAttribute('focusable', 'false')
  prototype.setAttribute(MARKDOWN_LINK_ICON_ATTRIBUTE, kind)
  prototypeCache.set(kind, prototype)
  return prototype
}

/** True when `el` contains visible non-image text outside prepended icons and image wrappers. */
function hasVisibleNonImageContent(el: Element): boolean {
  for (const child of el.childNodes) {
    if (child.nodeType === Node.TEXT_NODE) {
      if (child.nodeValue && child.nodeValue.trim().length > 0)
        return true
      continue
    }
    if (child.nodeType !== Node.ELEMENT_NODE)
      continue
    const childEl = child as Element
    if (childEl.classList.contains(markdownLinkIconClass) || isMarkdownImageElement(childEl))
      continue
    if (hasVisibleNonImageContent(childEl))
      return true
  }
  return false
}

function findDirectLinkIcons(el: HTMLElement): Element[] {
  const icons: Element[] = []
  for (const child of el.children) {
    if (child.classList.contains(markdownLinkIconClass))
      icons.push(child)
  }
  return icons
}

/**
 * Ensure `el` starts with a prepended decorative Lucide SVG icon matching `kind`,
 * or remove any existing icon when `kind` is `undefined` or `el` has no visible
 * non-image content (empty links and image-only links).
 */
export function syncMarkdownLinkIcon(el: HTMLElement, kind: MarkdownLinkIconKind | undefined): void {
  const existing = findDirectLinkIcons(el)
  if (kind === undefined || !hasVisibleNonImageContent(el)) {
    for (const icon of existing)
      icon.remove()
    return
  }

  if (
    existing.length === 1
    && existing[0] === el.firstChild
    && existing[0].getAttribute(MARKDOWN_LINK_ICON_ATTRIBUTE) === kind
  ) {
    return
  }

  for (const icon of existing)
    icon.remove()
  el.prepend(getIconPrototype(kind).cloneNode(true))
}
