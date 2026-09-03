import type { Mock } from 'vitest'
import type { NewTabMenuItemsProps } from './NewTabMenuItems'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { WORKSPACE_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { activateBindings, unbindAll } from '~/lib/shortcuts/keybindings'
import { NewTabMenuItems } from './NewTabMenuItems'

// The hints read a module singleton that only `activateBindings` fills, so the
// shortcut cases arm it themselves and every case unbinds afterwards.
afterEach(() => {
  unbindAll()
})

type Handlers = { [K in 'onNewAgent' | 'onNewAgentAdvanced' | 'onNewTerminalWithShell' | 'onNewTerminalAdvanced']: Mock<NewTabMenuItemsProps[K]> }

function renderItems(overrides: {
  availableProviders?: AgentProvider[]
  availableShells?: string[]
  defaultShell?: string
  shortcuts?: boolean
  disabledReason?: string
} = {}) {
  const handlers: Handlers = {
    onNewAgent: vi.fn(),
    onNewAgentAdvanced: vi.fn(),
    onNewTerminalWithShell: vi.fn(),
    onNewTerminalAdvanced: vi.fn(),
  }
  // `in`, not `??`: the two "still unknown" cases pass an explicit `undefined`,
  // which a nullish default would silently replace with the populated list.
  const providers = 'availableProviders' in overrides ? overrides.availableProviders : [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX]
  const shells = 'availableShells' in overrides ? overrides.availableShells : ['/bin/zsh', '/bin/bash']
  const defaultShell = 'defaultShell' in overrides ? overrides.defaultShell : '/bin/zsh'
  const result = render(() => (
    <menu>
      <NewTabMenuItems
        availableProviders={providers}
        availableShells={shells}
        defaultShell={defaultShell}
        shortcuts={overrides.shortcuts}
        disabledReason={overrides.disabledReason}
        {...handlers}
      />
    </menu>
  ))
  return { ...handlers, ...result }
}

/** The `<button>` of the item labelled `label` — the text node's own element is not it. */
function item(label: string): HTMLElement {
  const el = screen.getByText(label).closest('button')
  expect(el).not.toBeNull()
  return el!
}

describe('newTabMenuItems', () => {
  it('renders both section headers', () => {
    renderItems()
    expect(screen.getByText('Agents')).toBeInTheDocument()
    expect(screen.getByText('Terminals')).toBeInTheDocument()
  })

  describe('the provider glyph row', () => {
    it('renders one button per provider', () => {
      renderItems()
      expect(screen.getByTestId(`menu-new-agent-${AgentProvider.CLAUDE_CODE}`)).toBeInTheDocument()
      expect(screen.getByTestId(`menu-new-agent-${AgentProvider.CODEX}`)).toBeInTheDocument()
    })

    it('opens the clicked provider directly', async () => {
      const { onNewAgent, onNewAgentAdvanced } = renderItems()
      await fireEvent.click(screen.getByTestId(`menu-new-agent-${AgentProvider.CODEX}`))
      expect(onNewAgent).toHaveBeenCalledTimes(1)
      expect(onNewAgent).toHaveBeenCalledWith(AgentProvider.CODEX)
      expect(onNewAgentAdvanced).not.toHaveBeenCalled()
    })

    // An empty row would still take a gap slot in the menu and say nothing.
    it('renders no row for an empty list', () => {
      renderItems({ availableProviders: [] })
      expect(screen.queryByTestId(`menu-new-agent-${AgentProvider.CLAUDE_CODE}`)).toBeNull()
      expect(screen.getByText('Agents')).toBeInTheDocument()
    })

    it('renders no row while the list is still unknown', () => {
      renderItems({ availableProviders: undefined })
      expect(screen.queryByTestId(`menu-new-agent-${AgentProvider.CLAUDE_CODE}`)).toBeNull()
    })
  })

  describe('the shell list', () => {
    it('renders one item per shell and marks the default', () => {
      renderItems()
      expect(item('/bin/zsh').textContent).toContain('(default)')
      expect(item('/bin/bash').textContent).not.toContain('(default)')
    })

    it('opens the clicked shell directly', async () => {
      const { onNewTerminalWithShell, onNewTerminalAdvanced } = renderItems()
      await fireEvent.click(item('/bin/bash'))
      expect(onNewTerminalWithShell).toHaveBeenCalledTimes(1)
      expect(onNewTerminalWithShell).toHaveBeenCalledWith('/bin/bash')
      expect(onNewTerminalAdvanced).not.toHaveBeenCalled()
    })

    it('renders no shell item for an empty list', () => {
      renderItems({ availableShells: [] })
      expect(screen.queryByText('/bin/zsh')).toBeNull()
      expect(screen.getByText('Terminals')).toBeInTheDocument()
    })

    // Nothing is the worker's default while the list has not landed, so no item
    // may claim the marker.
    it('marks nothing when no default is reported', () => {
      renderItems({ defaultShell: undefined })
      expect(screen.queryByText('(default)')).toBeNull()
    })
  })

  it('routes the two dialog items to their own handlers', async () => {
    const { onNewAgentAdvanced, onNewTerminalAdvanced } = renderItems()
    await fireEvent.click(item('New agent...'))
    await fireEvent.click(item('New terminal...'))
    expect(onNewAgentAdvanced).toHaveBeenCalledTimes(1)
    expect(onNewTerminalAdvanced).toHaveBeenCalledTimes(1)
  })

  // The hints name app.newAgentDialog / app.newTerminalDialog, which act on the
  // CURRENT tab context. A surface that acts on something else asks for none.
  describe('shortcut hints', () => {
    it('adds a hint beside each dialog item when asked', () => {
      activateBindings(WORKSPACE_KEYBINDINGS, 'workspace')
      renderItems({ shortcuts: true })
      expect(item('New agent...').textContent).toMatch(/New agent\.\.\..+/)
      expect(item('New terminal...').textContent).toMatch(/New terminal\.\.\..+/)
    })

    it('omits them by default, even with the bindings armed', () => {
      activateBindings(WORKSPACE_KEYBINDINGS, 'workspace')
      renderItems()
      expect(item('New agent...').textContent).toBe('New agent...')
      expect(item('New terminal...').textContent).toBe('New terminal...')
    })
  })

  describe('when a reason is given', () => {
    const reason = 'This Worker is offline.'

    it('disables every item and describes it with the reason', () => {
      renderItems({ disabledReason: reason })
      const controls = [
        screen.getByTestId(`menu-new-agent-${AgentProvider.CLAUDE_CODE}`),
        screen.getByTestId(`menu-new-agent-${AgentProvider.CODEX}`),
        item('New agent...'),
        item('New terminal...'),
        item('/bin/zsh'),
        item('/bin/bash'),
      ]
      for (const control of controls) {
        expect(control).toBeDisabled()
        // Through the Tooltip's offscreen description, never `title`: a reason
        // on `title` becomes the control's accessible name.
        expect(control).not.toHaveAttribute('title')
        const describedBy = control.getAttribute('aria-describedby')
        expect(describedBy).toBeTruthy()
        expect(document.getElementById(describedBy!)?.textContent).toBe(reason)
      }
    })

    it('fires nothing while disabled', async () => {
      const handlers = renderItems({ disabledReason: reason })
      await fireEvent.click(screen.getByTestId(`menu-new-agent-${AgentProvider.CODEX}`))
      await fireEvent.click(item('New agent...'))
      await fireEvent.click(item('New terminal...'))
      await fireEvent.click(item('/bin/zsh'))
      expect(handlers.onNewAgent).not.toHaveBeenCalled()
      expect(handlers.onNewAgentAdvanced).not.toHaveBeenCalled()
      expect(handlers.onNewTerminalAdvanced).not.toHaveBeenCalled()
      expect(handlers.onNewTerminalWithShell).not.toHaveBeenCalled()
    })

    it('leaves everything enabled without one', () => {
      renderItems()
      expect(screen.getByTestId(`menu-new-agent-${AgentProvider.CODEX}`)).toBeEnabled()
      expect(item('New agent...')).toBeEnabled()
      expect(item('/bin/zsh')).toBeEnabled()
    })
  })
})
