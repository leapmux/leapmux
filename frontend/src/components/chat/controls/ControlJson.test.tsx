import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installControllableResizeObserver, triggerResizeObservers } from '~/test-support/resizeObserverStub'
import { ControlJson } from './ControlJson'

const originalObserver = globalThis.ResizeObserver
beforeEach(() => installControllableResizeObserver())
afterEach(() => {
  vi.restoreAllMocks()
  globalThis.ResizeObserver = originalObserver
})

describe('control JSON', () => {
  it('reformats when the panel width changes and preserves serialized numbers', async () => {
    let width = 240
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(() => width)
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width: 80 } as DOMRect)
    const raw = '{"query":"answer","path":"sample.py","limit":20,"id":9007199254740993}'
    const { container, unmount } = render(() => <ControlJson value={raw} maxLines={20} />)
    expect(container.querySelector('pre')?.textContent).toContain('\n    ')
    expect(container.querySelector('pre')?.textContent).toContain('9007199254740993')
    width = 1000
    await triggerResizeObservers()
    expect(container.querySelector('pre')?.textContent?.trim().split('\n')).toHaveLength(1)
    unmount()
    await triggerResizeObservers()
  })

  it('handles zero width and restores JSON that arrives after mounting', async () => {
    let width = 0
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(() => width)
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width: 80 } as DOMRect)
    const [value, setValue] = createSignal<unknown>(undefined)
    const { container } = render(() => <ControlJson value={value()} hideEmpty maxLines={1} />)
    expect(container.querySelector('pre')).toBeNull()
    setValue({ query: 'answer', path: 'sample.py', limit: 20 })
    expect(container.querySelector('pre')?.textContent).toContain('"query"')
    width = 240
    await triggerResizeObservers()
    fireEvent.click(screen.getByRole('button'))
    expect(JSON.parse(container.querySelector('pre')!.textContent!)).toEqual({ query: 'answer', path: 'sample.py', limit: 20 })
  })

  it('retains the serialization-error fallback', () => {
    const value: Record<string, unknown> = {}
    value.self = value
    const { container } = render(() => <ControlJson value={value} />)
    expect(container.querySelector('pre')?.textContent).toBe('{}')
  })
})
