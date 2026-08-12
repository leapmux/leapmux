import type { FileAttachment } from '~/components/chat/attachments'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AttachmentStrip } from '~/components/chat/AttachmentStrip'
import { clippedText } from '~/styles/shared.css'
import { hoverForTooltip, stubClipped, stubFitting } from '~/test-support/clipStub'
import { classSelector } from '~/test-support/composedClass'

function makeAttachment(overrides: Partial<FileAttachment> = {}): FileAttachment {
  return {
    id: crypto.randomUUID(),
    file: new File([], 'test.png'),
    filename: 'test.png',
    mimeType: 'image/png',
    data: new Uint8Array([0x89, 0x50]),
    size: 100,
    ...overrides,
  }
}

describe('attachmentStrip', () => {
  it('renders nothing when attachments is empty', () => {
    const [attachments] = createSignal<FileAttachment[]>([])
    const { container } = render(() => (
      <AttachmentStrip attachments={attachments} onRemove={() => {}} />
    ))
    expect(container).toBeEmptyDOMElement()
  })

  it('renders pills for each attachment', () => {
    const items = [
      makeAttachment({ id: 'a1', filename: 'photo.png', mimeType: 'image/png' }),
      makeAttachment({ id: 'a2', filename: 'report.pdf', mimeType: 'application/pdf' }),
    ]
    const [attachments] = createSignal<FileAttachment[]>(items)
    const { container } = render(() => (
      <AttachmentStrip attachments={attachments} onRemove={() => {}} />
    ))
    const pills = container.querySelectorAll('[data-testid="attachment-pill"]')
    expect(pills.length).toBe(2)
  })

  it('shows correct filenames in pills', () => {
    const items = [
      makeAttachment({ id: 'a1', filename: 'screenshot.png' }),
      makeAttachment({ id: 'a2', filename: 'document.pdf', mimeType: 'application/pdf' }),
    ]
    const [attachments] = createSignal<FileAttachment[]>(items)
    const { container } = render(() => (
      <AttachmentStrip attachments={attachments} onRemove={() => {}} />
    ))
    const pills = container.querySelectorAll('[data-testid="attachment-pill"]')
    expect(pills[0]).toHaveTextContent('screenshot.png')
    expect(pills[1]).toHaveTextContent('document.pdf')
  })

  it('provides the full filename to the tooltip trigger', () => {
    const items = [
      makeAttachment({ id: 'a1', filename: 'very/long/nested/path/to/screenshot.png' }),
    ]
    const [attachments] = createSignal<FileAttachment[]>(items)
    const { container } = render(() => (
      <AttachmentStrip attachments={attachments} onRemove={() => {}} />
    ))
    const filename = container.querySelector('[data-testid="attachment-pill"] > span:nth-of-type(2) > span') as HTMLSpanElement
    expect(filename).toBeInTheDocument()
    expect(filename).toHaveTextContent('very/long/nested/path/to/screenshot.png')
  })

  /**
   * The pill caps at 200px, so a long file name clips and the tooltip is the
   * only route to the rest. It used to show that tooltip UNCONDITIONALLY, which
   * repeated a name the reader could already see in full.
   */
  describe('filename tooltip', () => {
    beforeEach(() => {
      vi.useFakeTimers()
    })

    afterEach(() => {
      vi.useRealTimers()
      vi.restoreAllMocks()
    })

    function labelOf(container: HTMLElement): HTMLElement {
      return container.querySelector<HTMLElement>(classSelector(clippedText))!
    }

    it('gives the full filename on hover once the pill clips it', () => {
      const [attachments] = createSignal<FileAttachment[]>([
        makeAttachment({ id: 'a1', filename: 'a-screenshot-with-a-name-far-wider-than-the-pill.png' }),
      ])
      const { container } = render(() => (
        <AttachmentStrip attachments={attachments} onRemove={() => {}} />
      ))
      const label = labelOf(container)
      stubClipped(label)
      expect(hoverForTooltip(label)?.textContent).toBe('a-screenshot-with-a-name-far-wider-than-the-pill.png')
    })

    it('shows no tooltip while the filename fits', () => {
      const [attachments] = createSignal<FileAttachment[]>([
        makeAttachment({ id: 'a1', filename: 'a.png' }),
      ])
      const { container } = render(() => (
        <AttachmentStrip attachments={attachments} onRemove={() => {}} />
      ))
      const label = labelOf(container)
      stubFitting(label)
      expect(hoverForTooltip(label)).toBeNull()
    })
  })

  it('calls onRemove with the correct id when remove button is clicked', () => {
    const onRemove = vi.fn()
    const items = [
      makeAttachment({ id: 'remove-me', filename: 'test.png' }),
    ]
    const [attachments] = createSignal<FileAttachment[]>(items)
    const { container } = render(() => (
      <AttachmentStrip attachments={attachments} onRemove={onRemove} />
    ))
    const removeBtn = container.querySelector('[data-testid="attachment-remove"]') as HTMLButtonElement
    expect(removeBtn).toBeInTheDocument()
    removeBtn.click()
    expect(onRemove).toHaveBeenCalledWith('remove-me')
  })

  it('renders image icon for image mime types', () => {
    const items = [
      makeAttachment({ id: 'img', filename: 'photo.jpg', mimeType: 'image/jpeg' }),
    ]
    const [attachments] = createSignal<FileAttachment[]>(items)
    const { container } = render(() => (
      <AttachmentStrip attachments={attachments} onRemove={() => {}} />
    ))
    // The icon should be an SVG element inside the pill
    const pill = container.querySelector('[data-testid="attachment-pill"]')
    expect(pill).toBeInTheDocument()
    const svg = pill!.querySelector('svg')
    expect(svg).toBeInTheDocument()
  })

  it('sets up horizontal scroll on wheel event', () => {
    const items = [
      makeAttachment({ id: 'a1' }),
      makeAttachment({ id: 'a2' }),
      makeAttachment({ id: 'a3' }),
    ]
    const [attachments] = createSignal<FileAttachment[]>(items)
    const { container } = render(() => (
      <AttachmentStrip attachments={attachments} onRemove={() => {}} />
    ))
    const strip = container.querySelector('[data-testid="attachment-strip"]')
    expect(strip).toBeInTheDocument()
    // The strip element should exist and have the wheel handler attached
    // (we can't easily test scrollLeft changes in jsdom, but we verify the element exists)
    expect(strip!.children.length).toBe(3)
  })
})
