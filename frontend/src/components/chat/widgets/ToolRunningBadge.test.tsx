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
    <ToolRunningBadge toolProgress={progress} textSelectionActive={selecting} />
  ))
  return { ...result, setProgress, setSelecting }
}

describe('toolRunningBadge', () => {
  it('renders nothing when no tool is running', () => {
    const { container } = renderBadge(undefined)
    expect(container.textContent).toBe('')
  })

  it('renders nothing for an entry that reports neither an elapsed time nor a retry', () => {
    // A provider that reports a running tool but measures nothing (ZCode today)
    // must not produce an empty badge box.
    const { container } = renderBadge({ })
    expect(container.textContent).toBe('')
  })

  it('renders nothing for a zero elapsed time', () => {
    // The agent reports whole seconds, so 0 means "not measured yet".
    const { container } = renderBadge({ elapsedSeconds: 0 })
    expect(container.textContent).toBe('')
  })

  it('formats the elapsed time the way the result divider formats a duration', () => {
    for (const [seconds, text] of [[30, '30s'], [60, '1m'], [90, '1m 30s'], [3600, '1h'], [3690, '1h 1m 30s']] as const) {
      const { container, unmount } = renderBadge({ elapsedSeconds: seconds })
      expect(container.textContent).toBe(text)
      unmount()
    }
  })

  it('steps the elapsed time as heartbeats arrive', () => {
    const { container, setProgress } = renderBadge({ elapsedSeconds: 30 })
    expect(container.textContent).toBe('30s')
    setProgress({ elapsedSeconds: 60 })
    expect(container.textContent).toBe('1m')
  })

  it('shows the retry attempt instead of the elapsed time while a subagent retries', () => {
    const { container } = renderBadge({ elapsedSeconds: 90, retry: RETRY })
    expect(container.textContent).toContain('Retrying 2/5')
    expect(container.textContent).not.toContain('1m 30s')
  })

  it('falls back to the elapsed time once the retry resolves', () => {
    const { container, setProgress } = renderBadge({ elapsedSeconds: 90, retry: RETRY })
    expect(container.textContent).toContain('Retrying 2/5')
    setProgress({ elapsedSeconds: 90 })
    expect(container.textContent).toBe('1m 30s')
  })

  it('keeps the reason out of the badge itself -- only the attempt shows', () => {
    const { container } = renderBadge({ retry: RETRY })
    expect(container.textContent).toBe('Retrying 2/5')
    expect(container.textContent).not.toContain('overloaded')
  })

  // The selection guard. Replacing this text node while the user holds a
  // selection across it would collapse the selection.
  it('freezes on its last value while a text selection is live', () => {
    const { container, setProgress, setSelecting } = renderBadge({ elapsedSeconds: 30 })
    expect(container.textContent).toBe('30s')

    setSelecting(true)
    setProgress({ elapsedSeconds: 60 })
    expect(container.textContent).toBe('30s')
    setProgress({ elapsedSeconds: 90 })
    expect(container.textContent).toBe('30s')
  })

  it('flushes the latest value when the selection ends', () => {
    const { container, setProgress, setSelecting } = renderBadge({ elapsedSeconds: 30 })
    setSelecting(true)
    setProgress({ elapsedSeconds: 90 })
    expect(container.textContent).toBe('30s')

    setSelecting(false)
    // The memo tracks the selection signal too, so ending the selection re-runs
    // it and it returns the value it held back -- no separate flush.
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
    store.apply('a1', { spanId: 'toolu_A', elapsedSeconds: 30 })
    const [selecting, setSelecting] = createSignal(false)
    const { container } = render(() => (
      <ToolRunningBadge toolProgress={() => store.get('a1', 'toolu_A')} textSelectionActive={selecting} />
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
    store.apply('a1', { spanId: 'toolu_A', retry: RETRY })
    const [selecting, setSelecting] = createSignal(false)
    const { container } = render(() => (
      <ToolRunningBadge toolProgress={() => store.get('a1', 'toolu_A')} textSelectionActive={selecting} />
    ))
    expect(container.textContent).toBe('Retrying 2/5')

    setSelecting(true)
    store.apply('a1', { spanId: 'toolu_A', retry: { ...RETRY, attempt: 3 } })
    expect(container.textContent).toBe('Retrying 2/5')

    setSelecting(false)
    expect(container.textContent).toBe('Retrying 3/5')
  })

  /**
   * The two production teardown paths, driven reactively. `drop` runs when the
   * tool's result row lands and `clearAgent` at every turn/agent boundary; both
   * delete a key out from under a badge that is subscribed through it. A
   * non-reactive `get` returning undefined does not prove the mounted badge ever
   * hears about it -- the store must notify through the deleted parent.
   */
  it('disappears when the span is dropped, as it is when the result row lands', () => {
    const store = createToolProgressStore()
    store.apply('a1', { spanId: 'toolu_A', elapsedSeconds: 30 })
    const { container } = render(() => (
      <ToolRunningBadge toolProgress={() => store.get('a1', 'toolu_A')} />
    ))
    expect(container.textContent).toBe('30s')
    store.drop('a1', 'toolu_A')
    expect(container.textContent).toBe('')
  })

  it('disappears when the agent is cleared, as it is at every turn boundary', () => {
    const store = createToolProgressStore()
    store.apply('a1', { spanId: 'toolu_A', elapsedSeconds: 30 })
    const { container } = render(() => (
      <ToolRunningBadge toolProgress={() => store.get('a1', 'toolu_A')} />
    ))
    expect(container.textContent).toBe('30s')
    store.clearAgent('a1')
    expect(container.textContent).toBe('')
  })

  it('reappears when the same span starts running again', () => {
    // A second tool call can reuse a span id after a clear. The badge must come
    // back rather than stay dead because its subscription was torn down.
    const store = createToolProgressStore()
    store.apply('a1', { spanId: 'toolu_A', elapsedSeconds: 30 })
    const { container } = render(() => (
      <ToolRunningBadge toolProgress={() => store.get('a1', 'toolu_A')} />
    ))
    store.clearAgent('a1')
    expect(container.textContent).toBe('')
    store.apply('a1', { spanId: 'toolu_A', elapsedSeconds: 60 })
    expect(container.textContent).toBe('1m')
  })

  it('holds a badge that would have disappeared, until the selection ends', () => {
    const { container, setProgress, setSelecting } = renderBadge({ elapsedSeconds: 30 })
    setSelecting(true)
    setProgress(undefined)
    expect(container.textContent).toBe('30s')
    setSelecting(false)
    expect(container.textContent).toBe('')
  })

  /**
   * The freeze needs a value to hold, and on the FIRST run it has none. This is
   * not a rare state: the selection signal is chat-wide, so a user who selects
   * text and then scrolls mounts every new row with it already true.
   *
   * Without the guard the badge returns the memo's undefined `prev` on every run
   * and shows NOTHING for the whole life of the selection -- it never freezes on
   * a value, it never has one. The seeded first paint is safe for the reason the
   * freeze exists: it inserts a node rather than replacing one.
   */
  it('shows the live value when it mounts under a selection that is already live', () => {
    const [progress] = createSignal<ToolProgressEntry | undefined>({ elapsedSeconds: 90 })
    const { container } = render(() => (
      <ToolRunningBadge toolProgress={progress} textSelectionActive={() => true} />
    ))
    expect(container.textContent).toBe('1m 30s')
  })

  it('shows a retry that mounts under a selection that is already live', () => {
    const [progress] = createSignal<ToolProgressEntry | undefined>({ retry: RETRY })
    const { container } = render(() => (
      <ToolRunningBadge toolProgress={progress} textSelectionActive={() => true} />
    ))
    expect(container.textContent).toBe('Retrying 2/5')
  })

  it('still freezes every update AFTER the first, when it mounted under a selection', () => {
    const [progress, setProgress] = createSignal<ToolProgressEntry | undefined>({ elapsedSeconds: 30 })
    const { container } = render(() => (
      <ToolRunningBadge toolProgress={progress} textSelectionActive={() => true} />
    ))
    expect(container.textContent).toBe('30s')
    setProgress({ elapsedSeconds: 60 })
    expect(container.textContent).toBe('30s')
  })

  /**
   * One span, one text node, across the elapsed -> retry -> elapsed transition.
   *
   * A retry starting or resolving is a CONTENT change, not a structural one, so
   * it must not rebuild the node the user may be selecting across -- the same
   * property the store-backed test in toolRenderers.toolProgress.test.tsx pins
   * for a heartbeat. Two <Show> branches, one wrapped in a Tooltip and one not,
   * would build a fresh span and a fresh text node at each transition.
   */
  it('keeps its span and its text node when a retry starts and resolves', () => {
    const { getByTestId, setProgress } = renderBadge({ elapsedSeconds: 90 })
    const badge = getByTestId('tool-running-badge')
    const textNode = badge.firstChild
    expect(badge.textContent).toBe('1m 30s')

    setProgress({ elapsedSeconds: 90, retry: RETRY })
    expect(getByTestId('tool-running-badge')).toBe(badge)
    expect(badge.firstChild).toBe(textNode)
    expect(badge.textContent).toBe('Retrying 2/5')

    setProgress({ elapsedSeconds: 120 })
    expect(getByTestId('tool-running-badge')).toBe(badge)
    expect(badge.firstChild).toBe(textNode)
    expect(badge.textContent).toBe('2m')
  })

  /**
   * The agent reports whole seconds, so the badge must render whole seconds.
   * formatDuration -- which every OTHER duration in the chat uses -- switches to
   * one decimal below 10 seconds, and "5.0s" beside "30s" and "1m 30s" reads as
   * a different unit. Latent for Claude, whose first heartbeat is at 30 s, and
   * live for the first provider that reports a shorter one.
   */
  it('renders a short elapsed time in whole seconds, never as a decimal', () => {
    for (const [seconds, text] of [[1, '1s'], [5, '5s'], [9, '9s'], [10, '10s']] as const) {
      const { container, unmount } = renderBadge({ elapsedSeconds: seconds })
      expect(container.textContent).toBe(text)
      unmount()
    }
  })
})

describe('retryDetailText', () => {
  it('states the category, the status and the delay when the agent gave all three', () => {
    expect(retryDetailText(RETRY)).toBe('Retrying after overloaded · HTTP 529 · next attempt in 4.0s.')
  })

  it('omits a status the failure did not carry', () => {
    expect(retryDetailText({ ...RETRY, errorStatus: null })).toBe('Retrying after overloaded · next attempt in 4.0s.')
  })

  /**
   * `error_category` is the agent's own snake_case wire token, and this string
   * is a sentence a user reads. A tooltip that says "Retrying after
   * connection_error." shows the wire format to the user.
   *
   * The transformation stays generic -- underscores to spaces -- rather than a
   * table of one provider's tokens: each provider owns its own category set, and
   * a table of Claude's would not belong in this shared widget.
   */
  it('reads the category as words, never as its snake_case wire token', () => {
    for (const [category, words] of [
      ['connection_error', 'connection error'],
      ['server_error', 'server error'],
      ['authentication_failed', 'authentication failed'],
      ['rate_limit', 'rate limit'],
      ['overloaded', 'overloaded'],
    ] as const) {
      const text = retryDetailText({ ...RETRY, errorCategory: category, errorStatus: null, retryDelayMs: 0 })
      expect(text).toBe(`Retrying after ${words}.`)
      expect(text).not.toContain('_')
    }
  })

  it('omits a delay the agent did not report', () => {
    expect(retryDetailText({ ...RETRY, retryDelayMs: 0 })).toBe('Retrying after overloaded · HTTP 529.')
  })

  it('states the failure generically when the category is empty', () => {
    expect(retryDetailText({ attempt: 1, maxRetries: 3, retryDelayMs: 0, errorStatus: null, errorCategory: '' }))
      .toBe('Retrying after an API error.')
  })
})
