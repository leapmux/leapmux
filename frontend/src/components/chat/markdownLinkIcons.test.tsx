import type { MarkdownLinkIconKind } from './markdownLinkIcons'
import { describe, expect, it } from 'vitest'
import { LOCAL_IMAGE_CLASS, LOCAL_IMAGE_FALLBACK_CLASS } from '~/lib/localLinks'
import { markdownLinkIconClass } from './markdownEditor/markdownContent.css'
import { MARKDOWN_LINK_ICON_ATTRIBUTE, syncMarkdownLinkIcon } from './markdownLinkIcons'

describe('syncMarkdownLinkIcon', () => {
  it('prepends the matching Lucide svg for each icon kind on anchors and local link spans', () => {
    const kinds: MarkdownLinkIconKind[] = ['file', 'directory', 'image', 'remote']
    for (const kind of kinds) {
      const anchor = document.createElement('a')
      anchor.textContent = 'docs'
      syncMarkdownLinkIcon(anchor, kind)

      const anchorIcon = anchor.firstChild as Element | null
      expect(anchorIcon?.tagName.toLowerCase(), `anchor ${kind}`).toBe('svg')
      expect(anchorIcon?.classList.contains(markdownLinkIconClass), `anchor ${kind}`).toBe(true)
      expect(anchorIcon?.getAttribute(MARKDOWN_LINK_ICON_ATTRIBUTE), `anchor ${kind}`).toBe(kind)
      expect(anchorIcon?.getAttribute('aria-hidden'), `anchor ${kind}`).toBe('true')
      expect(anchorIcon?.getAttribute('focusable'), `anchor ${kind}`).toBe('false')
      expect(anchorIcon?.textContent, `anchor ${kind}`).toBe('')

      const span = document.createElement('span')
      span.textContent = 'guide.md'
      syncMarkdownLinkIcon(span, kind)

      const spanIcon = span.firstChild as Element | null
      expect(spanIcon?.tagName.toLowerCase(), `span ${kind}`).toBe('svg')
      expect(spanIcon?.classList.contains(markdownLinkIconClass), `span ${kind}`).toBe(true)
      expect(spanIcon?.getAttribute(MARKDOWN_LINK_ICON_ATTRIBUTE), `span ${kind}`).toBe(kind)
      expect(spanIcon?.getAttribute('aria-hidden'), `span ${kind}`).toBe('true')
      expect(spanIcon?.getAttribute('focusable'), `span ${kind}`).toBe('false')
      expect(spanIcon?.textContent, `span ${kind}`).toBe('')
    }
  })

  it('preserves exact node identity on idempotent re-sync and replaces or relocates cleanly without duplicates', () => {
    const el = document.createElement('span')
    el.textContent = 'docs'

    syncMarkdownLinkIcon(el, 'file')
    const firstSvg = el.firstChild as Element
    syncMarkdownLinkIcon(el, 'file')
    expect(el.firstChild).toBe(firstSvg)
    expect(el.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(1)

    syncMarkdownLinkIcon(el, 'directory')
    const dirIcons = el.querySelectorAll(`.${markdownLinkIconClass}`)
    expect(dirIcons).toHaveLength(1)
    expect(el.firstChild).toBe(dirIcons[0])
    expect(dirIcons[0]?.getAttribute(MARKDOWN_LINK_ICON_ATTRIBUTE)).toBe('directory')

    el.prepend(document.createTextNode('prefix '))
    expect(el.firstChild).not.toBe(dirIcons[0])
    syncMarkdownLinkIcon(el, 'directory')
    const relocated = el.querySelectorAll(`.${markdownLinkIconClass}`)
    expect(relocated).toHaveLength(1)
    expect(el.firstChild).toBe(relocated[0])
    expect(relocated[0]?.getAttribute(MARKDOWN_LINK_ICON_ATTRIBUTE)).toBe('directory')

    // Pre-existing duplicate icons are collapsed back to a single first-child icon.
    el.append(relocated[0]!.cloneNode(true))
    expect(el.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(2)
    syncMarkdownLinkIcon(el, 'directory')
    const deduped = el.querySelectorAll(`.${markdownLinkIconClass}`)
    expect(deduped).toHaveLength(1)
    expect(el.firstChild).toBe(deduped[0])
  })

  it('skips and removes icons on empty and image-only links while keeping icons on mixed image-plus-text links', () => {
    const empty = document.createElement('a')
    syncMarkdownLinkIcon(empty, 'remote')
    expect(empty.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)

    const whitespaceOnly = document.createElement('a')
    whitespaceOnly.textContent = '   \n\t  '
    syncMarkdownLinkIcon(whitespaceOnly, 'remote')
    expect(whitespaceOnly.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)

    const emptyNested = document.createElement('a')
    emptyNested.append(document.createElement('strong'))
    syncMarkdownLinkIcon(emptyNested, 'remote')
    expect(emptyNested.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)

    const nestedImgOnly = document.createElement('a')
    const nestedSpan = document.createElement('span')
    const nestedImg = document.createElement('img')
    nestedImg.src = 'data:image/png;base64,AA=='
    nestedImg.alt = 'nested'
    nestedSpan.append(nestedImg)
    nestedImgOnly.append(nestedSpan)
    syncMarkdownLinkIcon(nestedImgOnly, 'remote')
    expect(nestedImgOnly.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)

    const imgOnly = document.createElement('a')
    const directImg = document.createElement('img')
    directImg.src = 'data:image/png;base64,AA=='
    directImg.alt = 'x'
    imgOnly.append(directImg)
    syncMarkdownLinkIcon(imgOnly, 'remote')
    expect(imgOnly.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)

    const localImgOnly = document.createElement('a')
    const wrapper = document.createElement('span')
    wrapper.className = LOCAL_IMAGE_CLASS
    const fallback = document.createElement('span')
    fallback.className = LOCAL_IMAGE_FALLBACK_CLASS
    fallback.textContent = 'x'
    wrapper.append(fallback)
    localImgOnly.append(wrapper)
    syncMarkdownLinkIcon(localImgOnly, 'remote')
    expect(localImgOnly.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)

    const mixed = document.createElement('a')
    const mixedImg = document.createElement('img')
    mixedImg.src = 'data:image/png;base64,AA=='
    mixedImg.alt = 'x'
    mixed.append(mixedImg, document.createTextNode(' Docs'))
    syncMarkdownLinkIcon(mixed, 'remote')
    expect(mixed.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(1)
    expect((mixed.firstChild as Element).getAttribute(MARKDOWN_LINK_ICON_ATTRIBUTE)).toBe('remote')

    // Replacing a decorated anchor's text node with an <img> leaves the existing <svg>
    // attached until the next sync removes it.
    const transitioning = document.createElement('a')
    const textNode = document.createTextNode('Docs')
    transitioning.append(textNode)
    syncMarkdownLinkIcon(transitioning, 'remote')
    expect(transitioning.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(1)

    const replacementImg = document.createElement('img')
    replacementImg.src = 'data:image/png;base64,AA=='
    replacementImg.alt = 'x'
    textNode.replaceWith(replacementImg)
    expect(transitioning.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(1)

    syncMarkdownLinkIcon(transitioning, 'remote')
    expect(transitioning.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)

    const cleared = document.createElement('span')
    cleared.textContent = 'guide.md'
    syncMarkdownLinkIcon(cleared, 'file')
    expect(cleared.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(1)
    syncMarkdownLinkIcon(cleared, undefined)
    expect(cleared.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)
  })
})
