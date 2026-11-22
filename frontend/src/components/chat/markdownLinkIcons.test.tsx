import type { MarkdownLinkIconKind } from './markdownLinkIcons'
import { describe, expect, it } from 'vitest'
import { LOCAL_IMAGE_CLASS, LOCAL_IMAGE_FALLBACK_CLASS } from '~/lib/localLinks'
import { BLOCKED_IMAGE_CHIP_CLASS, BLOCKED_IMAGE_CLASS, BLOCKED_IMAGE_LABEL_CLASS } from '~/lib/rehypeBlockRemoteImages'
import { markdownLinkIconClass } from './markdownEditor/markdownContent.css'
import { MARKDOWN_LINK_ICON_ATTRIBUTE, syncMarkdownLinkIcon } from './markdownLinkIcons'

function makeDataImg(alt: string): HTMLImageElement {
  const img = document.createElement('img')
  img.src = 'data:image/png;base64,AA=='
  img.alt = alt
  return img
}

describe('syncMarkdownLinkIcon', () => {
  it('prepends the matching Lucide svg for each icon kind on anchors and local link spans', () => {
    const kinds: MarkdownLinkIconKind[] = ['file', 'directory', 'image', 'remote']
    const hosts = [
      ['a', 'docs'],
      ['span', 'guide.md'],
    ] as const
    for (const kind of kinds) {
      for (const [tag, label] of hosts) {
        const el = document.createElement(tag)
        el.textContent = label
        syncMarkdownLinkIcon(el, kind)

        const icon = el.firstChild as Element | null
        const desc = `${tag} ${kind}`
        expect(icon?.tagName.toLowerCase(), desc).toBe('svg')
        expect(icon?.classList.contains(markdownLinkIconClass), desc).toBe(true)
        expect(icon?.getAttribute(MARKDOWN_LINK_ICON_ATTRIBUTE), desc).toBe(kind)
        expect(icon?.getAttribute('aria-hidden'), desc).toBe('true')
        expect(icon?.getAttribute('focusable'), desc).toBe('false')
        expect(icon?.textContent, desc).toBe('')
      }
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
    nestedSpan.append(makeDataImg('nested'))
    nestedImgOnly.append(nestedSpan)
    syncMarkdownLinkIcon(nestedImgOnly, 'remote')
    expect(nestedImgOnly.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)

    const imgOnly = document.createElement('a')
    imgOnly.append(makeDataImg('x'))
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

    const blockedImgOnly = document.createElement('span')
    const blockedWrapper = document.createElement('span')
    blockedWrapper.className = BLOCKED_IMAGE_CLASS
    const blockedChip = document.createElement('span')
    blockedChip.className = BLOCKED_IMAGE_CHIP_CLASS
    blockedChip.textContent = 'remote image blocked'
    const blockedAnchor = document.createElement('a')
    blockedAnchor.className = BLOCKED_IMAGE_LABEL_CLASS
    blockedAnchor.href = 'https://evil.example/x.png'
    blockedAnchor.textContent = 'diagram'
    blockedWrapper.append(blockedChip, blockedAnchor)
    blockedImgOnly.append(blockedWrapper)
    syncMarkdownLinkIcon(blockedImgOnly, 'file')
    expect(blockedImgOnly.querySelectorAll(`.${markdownLinkIconClass}`)).toHaveLength(0)

    const mixed = document.createElement('a')
    mixed.append(makeDataImg('x'), document.createTextNode(' Docs'))
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

    textNode.replaceWith(makeDataImg('x'))
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
