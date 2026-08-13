import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { sortAgentProvidersByName } from '~/lib/agentProviders'
import { clippedText } from '~/styles/shared.css'
import { hoverForTooltip, stubClipped } from '~/test-support/clipStub'
import { classSelector } from '~/test-support/composedClass'

vi.mock('~/components/common/DropdownMenu', () => ({
  DropdownMenu: (props: any) => (
    <>
      {props.trigger({
        'aria-expanded': false,
        'ref': () => {},
        'onPointerDown': () => {},
        'onClick': () => {},
      })}
      <div>{props.children}</div>
    </>
  ),
}))

describe('agentProviderSelector', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('sorts providers alphabetically by label', () => {
    expect(sortAgentProvidersByName([
      AgentProvider.CODEX,
      AgentProvider.CLAUDE_CODE,
      AgentProvider.CURSOR,
    ])).toEqual([
      AgentProvider.CLAUDE_CODE,
      AgentProvider.CODEX,
      AgentProvider.CURSOR,
    ])
  })

  it('shows disabled empty state when no providers are available', () => {
    const [value] = createSignal(AgentProvider.CLAUDE_CODE)

    render(() => (
      <AgentProviderSelector
        value={value}
        onChange={() => {}}
        availableProviders={[]}
      />
    ))

    const trigger = screen.getByTestId('agent-provider-selector-trigger')
    expect(trigger).toHaveTextContent('No agents available')
    expect(trigger).toBeDisabled()
  })

  /**
   * The trigger caps its width, so a provider name can clip. The label is a
   * plain string on an ENABLED control, so the tooltip is reachable and the
   * clipping belongs to `ClippedText`.
   */
  describe('trigger label clipping', () => {
    beforeEach(() => {
      vi.useFakeTimers()
    })

    afterEach(() => {
      vi.useRealTimers()
      vi.restoreAllMocks()
    })

    // Scoped to the TRIGGER: the same provider name also labels its menu item.
    function labelOf(): HTMLElement {
      const trigger = screen.getByTestId('agent-provider-selector-trigger')
      return trigger.querySelector<HTMLElement>(classSelector(clippedText))!
    }

    it('holds the provider name to one clipped line', () => {
      const [value] = createSignal(AgentProvider.CLAUDE_CODE)
      render(() => (
        <AgentProviderSelector value={value} onChange={() => {}} availableProviders={[AgentProvider.CLAUDE_CODE]} />
      ))

      expect(labelOf().textContent).toBe('Claude Code')
    })

    it('gives the full provider name on hover once it is clipped', () => {
      const [value] = createSignal(AgentProvider.CLAUDE_CODE)
      render(() => (
        <AgentProviderSelector value={value} onChange={() => {}} availableProviders={[AgentProvider.CLAUDE_CODE]} />
      ))

      const label = labelOf()
      stubClipped(label)
      expect(hoverForTooltip(label)?.textContent).toBe('Claude Code')
    })

    // The empty state's label sits under `disabled`, which receives no pointer
    // events, so it keeps the raw style and gets no tooltip. Pinning that keeps
    // the exception deliberate rather than an oversight.
    it('leaves the disabled empty-state label without a tooltip', () => {
      const [value] = createSignal(AgentProvider.CLAUDE_CODE)
      const { container } = render(() => (
        <AgentProviderSelector value={value} onChange={() => {}} availableProviders={[]} />
      ))

      const label = screen.getByText('No agents available')
      expect(label.className.trim().split(/\s+/)).toContain(clippedText)
      // No Tooltip wrapper: the label is a direct child of the trigger's value
      // span, not of a `display: contents` wrapper.
      expect((label.parentElement as HTMLElement).style.display).not.toBe('contents')
      expect(container.querySelector('[role="tooltip"]')).toBeNull()
    })
  })

  it('renders icon-capable trigger and updates selection', async () => {
    const [value, setValue] = createSignal(AgentProvider.CODEX)
    const onChange = vi.fn((provider: AgentProvider) => setValue(provider))

    render(() => (
      <AgentProviderSelector
        value={value}
        onChange={onChange as any}
        availableProviders={[AgentProvider.CODEX, AgentProvider.CLAUDE_CODE]}
      />
    ))

    expect(screen.getByTestId('agent-provider-selector-trigger')).toHaveTextContent('Codex')

    await fireEvent.click(screen.getByTestId(`agent-provider-option-${AgentProvider.CLAUDE_CODE}`))

    expect(onChange).toHaveBeenCalledWith(AgentProvider.CLAUDE_CODE)
    expect(screen.getByTestId('agent-provider-selector-trigger')).toHaveTextContent('Claude Code')
  })

  // Each known AgentProvider value, when present in availableProviders, must
  // render an option with `agent-provider-option-${id}` testid and the right
  // human-readable label.
  it.each([
    [AgentProvider.CLAUDE_CODE, 'Claude Code'],
    [AgentProvider.CODEX, 'Codex'],
    [AgentProvider.OPENCODE, 'OpenCode'],
    [AgentProvider.GITHUB_COPILOT, 'GitHub Copilot'],
    [AgentProvider.CURSOR, 'Cursor'],
    [AgentProvider.GOOSE, 'Goose'],
    [AgentProvider.KILO, 'Kilo'],
    [AgentProvider.REASONIX, 'Reasonix'],
  ])('renders option for provider %d with label "%s"', (provider, label) => {
    const [value] = createSignal(provider as AgentProvider)
    render(() => (
      <AgentProviderSelector
        value={value}
        onChange={() => {}}
        availableProviders={[provider as AgentProvider]}
      />
    ))

    const option = screen.getByTestId(`agent-provider-option-${provider}`)
    expect(option).toHaveTextContent(label)
  })

  it('only renders options for available providers', () => {
    const [value] = createSignal(AgentProvider.CODEX)
    render(() => (
      <AgentProviderSelector
        value={value}
        onChange={() => {}}
        availableProviders={[AgentProvider.CODEX, AgentProvider.CLAUDE_CODE]}
      />
    ))

    expect(screen.queryByTestId(`agent-provider-option-${AgentProvider.OPENCODE}`)).toBeNull()
    expect(screen.queryByTestId(`agent-provider-option-${AgentProvider.GITHUB_COPILOT}`)).toBeNull()
  })
})
