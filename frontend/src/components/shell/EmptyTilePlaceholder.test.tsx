import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { EmptyTilePlaceholder } from './EmptyTilePlaceholder'

const NEW_AGENT_TEXT_RE = /new agent tab/i
const NEW_TERMINAL_TEXT_RE = /new terminal tab/i
const ARCHIVED_TEXT_RE = /workspace is archived/i
const NO_TABS_TEXT_RE = /no tabs in this tile/i

describe('emptyTilePlaceholder', () => {
  it('renders agent and terminal action buttons when actions are shown', () => {
    render(() => (
      <EmptyTilePlaceholder
        archived={false}
        showActions={true}
        onOpenAgent={() => {}}
        onOpenTerminal={() => {}}
      />
    ))

    const actions = screen.getByTestId('empty-tile-actions')
    expect(actions).toBeInTheDocument()

    const agentBtn = screen.getByTestId('empty-tile-open-agent')
    const terminalBtn = screen.getByTestId('empty-tile-open-terminal')
    expect(agentBtn).toHaveTextContent(NEW_AGENT_TEXT_RE)
    expect(terminalBtn).toHaveTextContent(NEW_TERMINAL_TEXT_RE)

    // Match the e2e assertion that no `title` attribute is set on the buttons.
    expect(agentBtn).not.toHaveAttribute('title')
    expect(terminalBtn).not.toHaveAttribute('title')
  })

  it('invokes onOpenAgent when the agent button is clicked', () => {
    const onOpenAgent = vi.fn()
    render(() => (
      <EmptyTilePlaceholder
        archived={false}
        showActions={true}
        onOpenAgent={onOpenAgent}
        onOpenTerminal={() => {}}
      />
    ))

    fireEvent.click(screen.getByTestId('empty-tile-open-agent'))
    expect(onOpenAgent).toHaveBeenCalledTimes(1)
  })

  it('invokes onOpenTerminal when the terminal button is clicked', () => {
    const onOpenTerminal = vi.fn()
    render(() => (
      <EmptyTilePlaceholder
        archived={false}
        showActions={true}
        onOpenAgent={() => {}}
        onOpenTerminal={onOpenTerminal}
      />
    ))

    fireEvent.click(screen.getByTestId('empty-tile-open-terminal'))
    expect(onOpenTerminal).toHaveBeenCalledTimes(1)
  })

  it('renders the no-tabs hint and no action buttons when showActions is false', () => {
    render(() => (
      <EmptyTilePlaceholder
        archived={false}
        showActions={false}
        onOpenAgent={() => {}}
        onOpenTerminal={() => {}}
      />
    ))

    const hint = screen.getByTestId('empty-tile-hint')
    expect(hint).toHaveTextContent(NO_TABS_TEXT_RE)
    expect(screen.queryByTestId('empty-tile-actions')).toBeNull()
    expect(screen.queryByTestId('empty-tile-open-agent')).toBeNull()
    expect(screen.queryByTestId('empty-tile-open-terminal')).toBeNull()
  })

  it('renders the archived placeholder regardless of showActions when archived is true', () => {
    render(() => (
      <EmptyTilePlaceholder
        archived={true}
        showActions={true}
        onOpenAgent={() => {}}
        onOpenTerminal={() => {}}
      />
    ))

    const archived = screen.getByTestId('tile-empty-state')
    expect(archived).toHaveTextContent(ARCHIVED_TEXT_RE)
    expect(screen.queryByTestId('empty-tile-actions')).toBeNull()
    expect(screen.queryByTestId('empty-tile-hint')).toBeNull()
  })

  it('does not invoke either callback when showActions is false', () => {
    const onOpenAgent = vi.fn()
    const onOpenTerminal = vi.fn()
    render(() => (
      <EmptyTilePlaceholder
        archived={false}
        showActions={false}
        onOpenAgent={onOpenAgent}
        onOpenTerminal={onOpenTerminal}
      />
    ))

    // No buttons exist to click; clicking the hint must not fire either callback.
    fireEvent.click(screen.getByTestId('empty-tile-hint'))
    expect(onOpenAgent).not.toHaveBeenCalled()
    expect(onOpenTerminal).not.toHaveBeenCalled()
  })
})
