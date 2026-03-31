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
})
