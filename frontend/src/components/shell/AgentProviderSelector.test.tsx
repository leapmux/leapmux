import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProviderSelector } from '~/components/shell/AgentProviderSelector'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { sortAgentProvidersByName } from '~/lib/agentProviders'

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
    [AgentProvider.GEMINI_CLI, 'Gemini CLI'],
    [AgentProvider.OPENCODE, 'OpenCode'],
    [AgentProvider.GITHUB_COPILOT, 'GitHub Copilot'],
    [AgentProvider.CURSOR, 'Cursor'],
    [AgentProvider.GOOSE, 'Goose'],
    [AgentProvider.KILO, 'Kilo'],
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

    expect(screen.queryByTestId(`agent-provider-option-${AgentProvider.GEMINI_CLI}`)).toBeNull()
    expect(screen.queryByTestId(`agent-provider-option-${AgentProvider.OPENCODE}`)).toBeNull()
    expect(screen.queryByTestId(`agent-provider-option-${AgentProvider.GITHUB_COPILOT}`)).toBeNull()
  })
})
