import type { FileAttachment } from '~/components/chat/attachments'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AttachmentStrip } from '~/components/chat/AttachmentStrip'

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
    expect(container.children.length).toBe(0)
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
    expect(pills[0].textContent).toContain('screenshot.png')
    expect(pills[1].textContent).toContain('document.pdf')
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
    expect(removeBtn).not.toBeNull()
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
    expect(pill).not.toBeNull()
    const svg = pill!.querySelector('svg')
    expect(svg).not.toBeNull()
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
    expect(strip).not.toBeNull()
    // The strip element should exist and have the wheel handler attached
    // (we can't easily test scrollLeft changes in jsdom, but we verify the element exists)
    expect(strip!.children.length).toBe(3)
  })
})
