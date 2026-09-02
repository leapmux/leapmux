/// <reference types="vitest/globals" />
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createToolProgressStore } from '~/stores/chatToolProgress'
import { retryDetailText, ToolRunningBadge } from './ToolRunningBadge'

const RETRY = { attempt: 2, maxRetries: 5, retryDelayMs: 4000, errorStatus: 529, errorCategory: 'overloaded' }

/**
 * Render the badge over signals, so a test can change the live progress or the
 * selection state and observe what the badge did with it.
 */
function renderBadge(initial?: ToolProgressEntry) {
  const [progress, setProgress] = createSignal<ToolProgressEntry | undefined>(initial)
  const [selecting, setSelecting] = createSignal(false)
  const result = render(() => (
    <ToolRunningBadge progress={progress} selectionActive={selecting} />
  ))
  return { ...result, setProgress, setSelecting }
}

describe('toolRunningBadge', () => {
  it('renders nothing when no tool is running', () => {
    const { container } = renderBadge(undefined)
    expect(container.textContent).toBe('')
  })

  it('renders nothing for an entry that reports neither an elapsed time nor a retry', () => {
    // A provider that names its running tool but measures nothing (ZCode today)
    // must not produce an empty badge box.
    const { container } = renderBadge({ toolName: 'Bash' })
    expect(container.textContent).toBe('')
  })

  it('renders nothing for a zero elapsed time', () => {
    // The agent reports whole seconds, so 0 means "not measured yet".
    const { container } = renderBadge({ toolName: 'Bash', elapsedSeconds: 0 })
    expect(container.textContent).toBe('')
  })

  it('formats the elapsed time the way the result divider formats a duration', () => {
    for (const [seconds, text] of [[30, '30s'], [60, '1m'], [90, '1m 30s'], [3600, '1h'], [3690, '1h 1m 30s']] as const) {
      const { container, unmount } = renderBadge({ toolName: 'Bash', elapsedSeconds: seconds })
      expect(container.textContent).toBe(text)
      unmount()
    }
  })

  it('steps the elapsed time as heartbeats arrive', () => {
    const { container, setProgress } = renderBadge({ toolName: 'Bash', elapsedSeconds: 30 })
    expect(container.textContent).toBe('30s')
    setProgress({ toolName: 'Bash', elapsedSeconds: 60 })
    expect(container.textContent).toBe('1m')
  })

  it('shows the retry attempt instead of the elapsed time while a subagent retries', () => {
    const { container } = renderBadge({ toolName: 'Agent', elapsedSeconds: 90, retry: RETRY })
    expect(container.textContent).toContain('Retrying 2/5')
    expect(container.textContent).not.toContain('1m 30s')
  })

  it('falls back to the elapsed time once the retry resolves', () => {
    const { container, setProgress } = renderBadge({ toolName: 'Agent', elapsedSeconds: 90, retry: RETRY })
    expect(container.textContent).toContain('Retrying 2/5')
    setProgress({ toolName: 'Agent', elapsedSeconds: 90 })
    expect(container.textContent).toBe('1m 30s')
  })

  it('keeps the reason out of the badge itself -- only the attempt shows', () => {
    const { container } = renderBadge({ toolName: 'Agent', retry: RETRY })
    expect(container.textContent).toBe('Retrying 2/5')
    expect(container.textContent).not.toContain('overloaded')
  })

  // The selection guard. Replacing this text node while the user holds a
  // selection across it would collapse the selection.
  it('freezes on its last value while a text selection is live', () => {
    const { container, setProgress, setSelecting } = renderBadge({ toolName: 'Bash', elapsedSeconds: 30 })
    expect(container.textContent).toBe('30s')

    setSelecting(true)
    setProgress({ toolName: 'Bash', elapsedSeconds: 60 })
    expect(container.textContent).toBe('30s')
    setProgress({ toolName: 'Bash', elapsedSeconds: 90 })
    expect(container.textContent).toBe('30s')
  })

  it('flushes the latest value when the selection ends', () => {
    const { container, setProgress, setSelecting } = renderBadge({ toolName: 'Bash', elapsedSeconds: 30 })
    setSelecting(true)
    setProgress({ toolName: 'Bash', elapsedSeconds: 90 })
    expect(container.textContent).toBe('30s')

    setSelecting(false)
    // The memo tracks the selection signal too, so ending the selection re-runs
    // it and it returns the value it had been holding back -- no separate flush.
    expect(container.textContent).toBe('1m 30s')
  })

  /**
   * The freeze must hold against the REAL store, which is what production wires
   * in. `getToolProgress` hands back a live store proxy, and merging a heartbeat
   * into an entry does NOT change that proxy's identity -- so a badge that held
   * the entry object itself would hold a reference that keeps reporting the
   * newest value. The freeze would then read as working against fresh object
   * literals (every test above) and do nothing at all in the app.
   */
  it('freezes against a live store entry, whose identity never changes', () => {
    const store = createToolProgressStore()
    store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 30 })
    const [selecting, setSelecting] = createSignal(false)
    const { container } = render(() => (
      <ToolRunningBadge progress={() => store.get('a1', 'toolu_A')} selectionActive={selecting} />
    ))
    expect(container.textContent).toBe('30s')

    setSelecting(true)
    store.apply('a1', { spanId: 'toolu_A', elapsedSeconds: 60 })
    expect(container.textContent).toBe('30s')

    setSelecting(false)
    expect(container.textContent).toBe('1m')
  })

  it('freezes a live store retry the same way', () => {
    const store = createToolProgressStore()
    store.apply('a1', { spanId: 'toolu_A', toolName: 'Agent', retry: RETRY })
    const [selecting, setSelecting] = createSignal(false)
    const { container } = render(() => (
      <ToolRunningBadge progress={() => store.get('a1', 'toolu_A')} selectionActive={selecting} />
    ))
    expect(container.textContent).toBe('Retrying 2/5')

    setSelecting(true)
    store.apply('a1', { spanId: 'toolu_A', retry: { ...RETRY, attempt: 3 } })
    expect(container.textContent).toBe('Retrying 2/5')

    setSelecting(false)
    expect(container.textContent).toBe('Retrying 3/5')
  })

  it('holds a badge that would have disappeared, until the selection ends', () => {
    const { container, setProgress, setSelecting } = renderBadge({ toolName: 'Bash', elapsedSeconds: 30 })
    setSelecting(true)
    setProgress(undefined)
    expect(container.textContent).toBe('30s')
    setSelecting(false)
    expect(container.textContent).toBe('')
  })
})

describe('retryDetailText', () => {
  it('states the category, the status and the delay when the agent gave all three', () => {
    expect(retryDetailText(RETRY)).toBe('Retrying after overloaded · HTTP 529 · next attempt in 4.0s.')
  })

  it('omits the status a connection error does not carry', () => {
    expect(retryDetailText({ ...RETRY, errorStatus: null })).toBe('Retrying after overloaded · next attempt in 4.0s.')
  })

  it('omits a delay the agent did not report', () => {
    expect(retryDetailText({ ...RETRY, retryDelayMs: 0 })).toBe('Retrying after overloaded · HTTP 529.')
  })

  it('names the failure generically when the category is empty', () => {
    expect(retryDetailText({ attempt: 1, maxRetries: 3, retryDelayMs: 0, errorStatus: null, errorCategory: '' }))
      .toBe('Retrying after an API error.')
  })
})
