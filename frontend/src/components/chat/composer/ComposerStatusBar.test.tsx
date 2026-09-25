import type { ProviderSettingChangeHandler } from '~/components/chat/providerSettings'
import type { AgentInfo, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import type { DiffStats } from '~/stores/repoGit'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { stubBranchMenuActions } from '~/test-support/branchMenu'
import { hoverForTooltip } from '~/test-support/clipStub'
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

function renderBar(
  a: AgentInfo | undefined,
  extra: {
    disabledReason?: string
    branchDisabledReason?: string
    branchName?: string
    isWorktree?: boolean
    directory?: string
    homeDir?: string
    branchStats?: DiffStats | null
    onSettingChange?: ProviderSettingChangeHandler
  } = {},
) {
  // The bar takes ONE value now, so the helper assembles it from the flat
  // options each case reads better in.
  const workingTree = {
    isWorktree: extra.isWorktree ?? false,
    name: extra.branchName ?? '',
    directory: extra.directory ?? '',
    ...(extra.homeDir === undefined ? {} : { homeDir: extra.homeDir }),
    ...(extra.branchStats === undefined ? {} : { stats: extra.branchStats }),
  }
  return render(() => (
    <ComposerStatusBar
      {...(a === undefined ? {} : { agent: a })}
      workingTree={workingTree}
      optionValues={{}}
      onSettingChange={extra.onSettingChange ?? (() => {})}
      branchActions={stubBranchMenuActions()}
      branchWorkerId="w-1"
      infoTrigger={() => <span data-testid="info" />}
      {...(extra.disabledReason === undefined ? {} : { disabledReason: extra.disabledReason })}
      {...(extra.branchDisabledReason === undefined ? {} : { branchDisabledReason: extra.branchDisabledReason })}
    />
  ))
}

describe('ComposerStatusBar', () => {
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

  it('draws the effort chip from the provider-declared effort axis', () => {
    // Goose reports its reasoning axis under its own id, and the chip follows it.
    renderBar(agent({
      agentProvider: AgentProvider.GOOSE,
      optionGroups: [group('thinking_effort', 'Thinking Effort', ['high', 'low'])],
    } as Partial<AgentInfo>))

    expect(screen.getByTestId('composer-effort-trigger')).toHaveTextContent('high')
  })

  it('draws no effort chip for a group that is not the provider\'s effort axis', () => {
    // Claude's effort axis is the well-known id, so another provider's id draws no
    // chip, and Goose's own axis is not the well-known id.
    renderBar(agent({
      agentProvider: AgentProvider.CLAUDE_CODE,
      optionGroups: [group('reasoning_effort', 'Reasoning Effort', ['high'])],
    } as Partial<AgentInfo>))
    expect(screen.queryByTestId('composer-effort-trigger')).toBeNull()
  })

  it('sends an effort change under the provider-declared axis id', async () => {
    // The chip and the setting it sends must name one axis. A chip that drew
    // Goose's axis and sent the well-known id would change nothing on the agent.
    const onSettingChange = vi.fn()
    renderBar(agent({
      agentProvider: AgentProvider.GOOSE,
      optionGroups: [group('thinking_effort', 'Thinking Effort', ['high', 'low'])],
    } as Partial<AgentInfo>), { onSettingChange })

    fireEvent.click(screen.getByTestId('composer-effort-trigger'))
    await fireEvent.click(screen.getByTestId('composer-effort-low'))
    expect(onSettingChange).toHaveBeenCalledWith({ sets: { thinking_effort: 'low' } })
  })

  it('ignores a well-known effort group for a provider that declares its own axis', () => {
    renderBar(agent({
      agentProvider: AgentProvider.GOOSE,
      optionGroups: [group('effort', 'Effort', ['high'])],
    } as Partial<AgentInfo>))
    expect(screen.queryByTestId('composer-effort-trigger')).toBeNull()
  })

  it('disables every chip when the composer accepts no input', async () => {
    renderBar(
      agent({ optionGroups: [group('model', 'Model', ['opus', 'sonnet'])] } as Partial<AgentInfo>),
      { disabledReason: 'This subagent doesn\'t accept messages.' },
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

  it('shows the branch chip only when branchName is provided', () => {
    renderBar(agent())
    expect(screen.queryByText('main')).toBeNull()

    renderBar(agent(), { branchName: 'main' })
    expect(screen.getByText('main')).toBeInTheDocument()
  })

  it('uses the branchName prop for the chip label', () => {
    renderBar(agent(), { branchName: 'renamed' })
    expect(screen.getByText('renamed')).toBeInTheDocument()
    expect(screen.queryByText('main')).toBeNull()
  })

  it('hides the branch chip when branchName is explicitly empty', () => {
    renderBar(agent(), { branchName: '' })
    expect(screen.queryByTestId('composer-branch-trigger')).toBeNull()
    expect(screen.queryByText('main')).toBeNull()
  })

  // The bar owns no git state of its own: it forwards what the panel resolved
  // from the repo store, so the chip can name the kind of checkout. Dropping
  // one of these silently repaints every worktree chip as a branch.
  it('forwards the checkout kind to the chip', () => {
    renderBar(agent(), { branchName: 'feature', isWorktree: true, directory: '/home/dev/wt' })

    const chip = screen.getByTestId('composer-branch-trigger')
    expect(chip.querySelector('[data-testid="worktree-icon"]')).not.toBeNull()
  })

  it('defaults the chip to a branch when the kind is not known yet', () => {
    renderBar(agent(), { branchName: 'feature' })

    const chip = screen.getByTestId('composer-branch-trigger')
    expect(chip.querySelector('[data-testid="branch-icon"]')).not.toBeNull()
  })

  // The chip renders the other three git props only inside its tooltip, so the
  // hover is the only place the wiring shows. A swap between `directory` and
  // `homeDir` leaves the path absolute, and a dropped `branchStats` leaves the
  // badge out -- neither one changes anything the tests above look at.
  describe('forwarding the git props the tooltip renders', () => {
    beforeAll(() => {
      // The tooltip enters the top layer when it opens.
      HTMLElement.prototype.showPopover = vi.fn()
      HTMLElement.prototype.hidePopover = vi.fn()
    })

    // Scoped to this block: `hoverForTooltip` runs out the show delay on a fake
    // clock, and the tests above drive real user events.
    beforeEach(() => {
      vi.useFakeTimers()
    })

    afterEach(() => {
      vi.useRealTimers()
    })

    it('hands the chip the directory, the home dir and the diff stats', () => {
      renderBar(agent(), {
        branchName: 'feature',
        isWorktree: true,
        directory: '/home/dev/repos/leapmux-worktrees/feature',
        homeDir: '/home/dev',
        branchStats: { added: 38, deleted: 12, untracked: 0 },
      })

      const tooltip = hoverForTooltip(screen.getByTestId('composer-branch-trigger'))
      expect(tooltip).not.toBeNull()
      expect(tooltip!.querySelector('[data-testid="working-tree-directory"]'))
        .toHaveTextContent('~/repos/leapmux-worktrees/feature')
      expect(tooltip!.querySelector('[data-testid="git-diff-stats"]')).toHaveTextContent('+38')
    })
  })
})
