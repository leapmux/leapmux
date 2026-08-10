import type { AgentInfo, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ComposerStatusBar } from './ComposerStatusBar'
import '~/components/chat/providers'

function group(id: string, label: string, optionIds: string[], currentValue = optionIds[0]): AvailableOptionGroup {
  return {
    id,
    label,
    mutable: true,
    order: 0,
    defaultValue: optionIds[0] ?? '',
    currentValue,
    options: optionIds.map(o => ({ id: o, name: o })),
  } as unknown as AvailableOptionGroup
}

function agent(overrides: Partial<AgentInfo> = {}): AgentInfo {
  return {
    agentProvider: AgentProvider.CLAUDE_CODE,
    optionGroups: [],
    ...overrides,
  } as unknown as AgentInfo
}

function renderBar(a: AgentInfo | undefined, extra: { disabled?: boolean, branchDisabledReason?: string } = {}) {
  return render(() => (
    <ComposerStatusBar
      agent={a}
      optionValues={{}}
      onSettingChange={() => {}}
      onChangeBranch={() => {}}
      onDeleteBranch={() => {}}
      infoTrigger={() => <span data-testid="info" />}
      {...extra}
    />
  ))
}

describe('composerStatusBar', () => {
  it('renders a chip for each axis the agent offers', () => {
    renderBar(agent({
      optionGroups: [
        group('model', 'Model', ['opus', 'sonnet']),
        group('effort', 'Effort', ['high', 'low']),
        group('permissionMode', 'Mode', ['default', 'plan']),
      ],
    } as Partial<AgentInfo>))

    expect(screen.getByTestId('composer-model-trigger')).toHaveTextContent('opus')
    expect(screen.getByTestId('composer-effort-trigger')).toHaveTextContent('high')
    expect(screen.getByTestId('composer-mode-trigger')).toHaveTextContent('default')
  })

  it('hides a chip whose group is absent', () => {
    renderBar(agent({ optionGroups: [group('model', 'Model', ['opus'])] } as Partial<AgentInfo>))

    expect(screen.getByTestId('composer-model-trigger')).toBeInTheDocument()
    expect(screen.queryByTestId('composer-effort-trigger')).toBeNull()
    expect(screen.queryByTestId('composer-mode-trigger')).toBeNull()
  })

  it('hides a chip whose group exists but offers no options', () => {
    // A transient catalog on first handshake reports the group with an empty
    // option list; a chip with nothing to pick is a dead control.
    renderBar(agent({ optionGroups: [group('model', 'Model', [])] } as Partial<AgentInfo>))

    expect(screen.queryByTestId('composer-model-trigger')).toBeNull()
  })

  it('renders no chips at all when there is no agent', () => {
    renderBar(undefined)

    expect(screen.queryByTestId('composer-model-trigger')).toBeNull()
    expect(screen.getByTestId('composer-status-bar')).toBeInTheDocument()
  })

  it('uses the provider-declared mode axis rather than a hardcoded key', () => {
    // Codex calls its mode axis `collaboration_mode`, not `permissionMode`.
    renderBar(agent({
      agentProvider: AgentProvider.CODEX,
      optionGroups: [group('collaboration_mode', 'Mode', ['chat', 'agent'])],
    } as Partial<AgentInfo>))

    expect(screen.getByTestId('composer-mode-trigger')).toHaveTextContent('chat')
  })

  it('disables every chip when the composer accepts no input', async () => {
    renderBar(
      agent({ optionGroups: [group('model', 'Model', ['opus', 'sonnet'])] } as Partial<AgentInfo>),
      { disabled: true },
    )

    expect(screen.getByTestId('composer-model-sonnet')).toBeDisabled()
  })

  it('leaves the in-flight settings marker to the [+] menu', () => {
    // The marker cannot live here: this bar is a preference the `[+]` menu can
    // switch off, which would take the only feedback for a pending change with
    // it. ComposerPlusMenu.test.tsx owns that assertion.
    renderBar(agent())
    expect(screen.queryByTestId('settings-loading-spinner')).toBeNull()
  })

  it('marks only the chips it drops first on a narrow composer', () => {
    // The responsive stylesheet hooks this attribute rather than a test id, so
    // the drop rule lives in one place. Two tiers: Branch and Model always
    // stay, Mode and Effort both drop at the `sm` breakpoint.
    renderBar(agent({
      optionGroups: [
        group('model', 'Model', ['opus']),
        group('effort', 'Effort', ['high']),
        group('permissionMode', 'Mode', ['default']),
      ],
    } as Partial<AgentInfo>))

    expect(screen.getByTestId('composer-model-trigger')).not.toHaveAttribute('data-chip-optional')
    expect(screen.getByTestId('composer-effort-trigger')).toHaveAttribute('data-chip-optional')
    expect(screen.getByTestId('composer-mode-trigger')).toHaveAttribute('data-chip-optional')
  })

  it('always renders the info trigger', () => {
    renderBar(agent())
    expect(screen.getByTestId('info')).toBeInTheDocument()
  })

  it('shows the branch chip only when the agent reports a branch', () => {
    renderBar(agent())
    expect(screen.queryByText('main')).toBeNull()

    renderBar(agent({ gitStatus: { branch: 'main' } } as unknown as Partial<AgentInfo>))
    expect(screen.getByText('main')).toBeInTheDocument()
  })
})
