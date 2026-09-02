import type { RenderContext } from './messageRenderers'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import { render } from '@solidjs/testing-library'
import SquareTerminal from 'lucide-solid/icons/square-terminal'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createToolProgressStore } from '~/stores/chatToolProgress'
import { ToolUseLayout } from './toolRenderers'

/**
 * Render a tool card over a live progress signal, the way a real row does: the
 * context hands the layout a THUNK, and only the badge inside calls it.
 */
function renderCard(initial?: ToolProgressEntry) {
  const [progress, setProgress] = createSignal<ToolProgressEntry | undefined>(initial)
  const [selecting, setSelecting] = createSignal(false)
  const context = {
    toolProgress: progress,
    textSelectionActive: selecting,
  } as unknown as RenderContext
  const result = render(() => (
    <ToolUseLayout
      icon={SquareTerminal}
      toolName="Bash"
      title="npm run build"
      summary={<span data-testid="summary">the summary line</span>}
      context={context}
    />
  ))
  return { ...result, setProgress, setSelecting }
}

describe('toolUseLayout running-tool badge', () => {
  it('renders the elapsed time in the card header', () => {
    const { container } = renderCard({ toolName: 'Bash', elapsedSeconds: 90 })
    expect(container.textContent).toContain('npm run build')
    expect(container.textContent).toContain('1m 30s')
  })

  it('renders no badge when nothing is running', () => {
    const { container } = renderCard(undefined)
    expect(container.textContent).toContain('npm run build')
    expect(container.textContent).not.toContain('30s')
  })

  /**
   * The property the whole design exists for. A progress update must replace the
   * badge's text node and NOTHING else -- if it re-rendered the card, a text
   * selection the user is holding across the title or the summary would collapse.
   *
   * Asserted as DOM node identity rather than as "the text still reads the same",
   * because a re-render produces identical text in fresh nodes and would pass a
   * text-only assertion while breaking the selection.
   */
  it('replaces no node outside the badge when the progress updates', () => {
    const { container, getByText, getByTestId, setProgress } = renderCard({ toolName: 'Bash', elapsedSeconds: 30 })
    const title = getByText('npm run build')
    const summary = getByTestId('summary')
    const icon = container.querySelector('svg')
    expect(container.textContent).toContain('30s')

    setProgress({ toolName: 'Bash', elapsedSeconds: 60 })

    expect(container.textContent).toContain('1m')
    expect(getByText('npm run build')).toBe(title)
    expect(getByTestId('summary')).toBe(summary)
    expect(container.querySelector('svg')).toBe(icon)
  })

  it('replaces no node outside the badge when a tool starts reporting mid-render', () => {
    // The badge appears from nothing on the first heartbeat, 30 seconds into the
    // tool call -- a moment the user is very likely to be reading the card.
    const { getByText, getByTestId, setProgress } = renderCard(undefined)
    const title = getByText('npm run build')
    const summary = getByTestId('summary')

    setProgress({ toolName: 'Bash', elapsedSeconds: 30 })

    expect(getByText('npm run build')).toBe(title)
    expect(getByTestId('summary')).toBe(summary)
  })

  it('writes nothing at all while a text selection is live', () => {
    const { container, setProgress, setSelecting } = renderCard({ toolName: 'Bash', elapsedSeconds: 30 })
    setSelecting(true)
    setProgress({ toolName: 'Bash', elapsedSeconds: 60 })
    expect(container.textContent).toContain('30s')
    expect(container.textContent).not.toContain('1m')
  })

  /**
   * The same two properties over the REAL store, which is what TileRenderer wires
   * in. A store update merges into an existing entry instead of replacing it, so
   * it exercises the fine-grained path the signal cases above cannot reach.
   */
  it('holds every node of the card and honours the selection over a live store', () => {
    const store = createToolProgressStore()
    store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 30 })
    const [selecting, setSelecting] = createSignal(false)
    const context = {
      toolProgress: () => store.get('a1', 'toolu_A'),
      textSelectionActive: selecting,
    } as unknown as RenderContext
    const { getByText, getByTestId } = render(() => (
      <ToolUseLayout
        icon={SquareTerminal}
        toolName="Bash"
        title="npm run build"
        summary={<span data-testid="summary">the summary line</span>}
        context={context}
      />
    ))
    const title = getByText('npm run build')
    const summary = getByTestId('summary')
    const badge = getByTestId('tool-running-badge')
    expect(badge.textContent).toBe('30s')

    store.apply('a1', { spanId: 'toolu_A', elapsedSeconds: 60 })
    expect(badge.textContent).toBe('1m')
    expect(getByText('npm run build')).toBe(title)
    expect(getByTestId('summary')).toBe(summary)
    // Even the badge's own element survives: only its text node is rewritten.
    expect(getByTestId('tool-running-badge')).toBe(badge)

    setSelecting(true)
    store.apply('a1', { spanId: 'toolu_A', elapsedSeconds: 90 })
    expect(badge.textContent).toBe('1m')
  })
})
