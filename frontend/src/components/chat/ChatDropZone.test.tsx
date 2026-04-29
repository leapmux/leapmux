import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ChatDropZone } from './ChatDropZone'

interface FakeDataTransfer {
  types: string[]
  files: File[]
  dropEffect: string
}

function fakeDataTransfer(files: File[] = [new File(['x'], 'x.png', { type: 'image/png' })]): FakeDataTransfer {
  return { types: ['Files'], files, dropEffect: '' }
}

function dispatchDragEvent(target: Element, type: string, dataTransfer: FakeDataTransfer): Event {
  const event = new Event(type, { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'dataTransfer', { value: dataTransfer })
  target.dispatchEvent(event)
  return event
}

describe('chatDropZone', () => {
  it('shows the overlay while dragging files over the zone and clears it on dragleave', () => {
    const { container, queryByText } = render(() => (
      <ChatDropZone>
        <div data-testid="inner">child</div>
      </ChatDropZone>
    ))

    const inner = container.querySelector('[data-testid="inner"]') as HTMLElement
    dispatchDragEvent(inner, 'dragenter', fakeDataTransfer())
    expect(queryByText('Drop files to attach')).toBeTruthy()

    dispatchDragEvent(inner, 'dragleave', fakeDataTransfer())
    expect(queryByText('Drop files to attach')).toBeFalsy()
  })

  it('clears the overlay after a drop bubbles up to the zone', () => {
    const onDrop = vi.fn()
    const { container, queryByText } = render(() => (
      <ChatDropZone onDrop={onDrop}>
        <div data-testid="inner">child</div>
      </ChatDropZone>
    ))

    const inner = container.querySelector('[data-testid="inner"]') as HTMLElement
    dispatchDragEvent(inner, 'dragenter', fakeDataTransfer())
    expect(queryByText('Drop files to attach')).toBeTruthy()

    dispatchDragEvent(inner, 'drop', fakeDataTransfer())

    expect(queryByText('Drop files to attach')).toBeFalsy()
    expect(onDrop).toHaveBeenCalledTimes(1)
  })

  it('clears the overlay even when an inner handler stops propagation', () => {
    // Reproduces the lingering-overlay bug: MarkdownEditor installs a
    // capture-phase drop listener that calls stopPropagation() to keep
    // dropped files out of ProseMirror, which previously prevented
    // ChatDropZone's bubble-phase drop handler from ever resetting the
    // overlay state.
    const onDrop = vi.fn()
    const { container, queryByText } = render(() => (
      <ChatDropZone onDrop={onDrop}>
        <div data-testid="inner">child</div>
      </ChatDropZone>
    ))

    const inner = container.querySelector('[data-testid="inner"]') as HTMLElement
    inner.addEventListener('drop', (e) => {
      e.preventDefault()
      e.stopPropagation()
    }, true)

    dispatchDragEvent(inner, 'dragenter', fakeDataTransfer())
    expect(queryByText('Drop files to attach')).toBeTruthy()

    dispatchDragEvent(inner, 'drop', fakeDataTransfer())

    expect(queryByText('Drop files to attach')).toBeFalsy()
    // The inner handler consumed the drop, so the outer onDrop must not fire.
    expect(onDrop).not.toHaveBeenCalled()
  })

  it('ignores drags that do not contain files', () => {
    const { container, queryByText } = render(() => (
      <ChatDropZone>
        <div data-testid="inner">child</div>
      </ChatDropZone>
    ))

    const inner = container.querySelector('[data-testid="inner"]') as HTMLElement
    dispatchDragEvent(inner, 'dragenter', { types: ['text/plain'], files: [], dropEffect: '' })
    expect(queryByText('Drop files to attach')).toBeFalsy()
  })

  it('does not show the overlay while disabled', () => {
    const { container, queryByText } = render(() => (
      <ChatDropZone disabled>
        <div data-testid="inner">child</div>
      </ChatDropZone>
    ))

    const inner = container.querySelector('[data-testid="inner"]') as HTMLElement
    dispatchDragEvent(inner, 'dragenter', fakeDataTransfer())
    expect(queryByText('Drop files to attach')).toBeFalsy()
  })

  it('does not invoke onDrop while disabled even when files are present', () => {
    const onDrop = vi.fn()
    const { container } = render(() => (
      <ChatDropZone disabled onDrop={onDrop}>
        <div data-testid="inner">child</div>
      </ChatDropZone>
    ))

    const inner = container.querySelector('[data-testid="inner"]') as HTMLElement
    dispatchDragEvent(inner, 'drop', fakeDataTransfer())
    expect(onDrop).not.toHaveBeenCalled()
  })
})
