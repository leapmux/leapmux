import type { RenameTabDeps } from './renameTab'
import type { AgentTab, Tab, TerminalTab } from '~/stores/tab.types'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { renameTab } from './renameTab'

const mockRenameAgent = vi.fn().mockResolvedValue({})
const mockUpdateTerminalTitle = vi.fn().mockResolvedValue({})
const mockShowWarnToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  renameAgent: (...args: unknown[]) => mockRenameAgent(...args),
  updateTerminalTitle: (...args: unknown[]) => mockUpdateTerminalTitle(...args),
}))

vi.mock('~/components/common/Toast', () => ({
  showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
}))

function agentTab(fields: Partial<AgentTab> = {}): Tab {
  return {
    type: TabType.AGENT,
    id: 'a1',
    workspaceId: 'ws-1',
    position: 'a',
    workerId: 'worker-1',
    ...fields,
  } as Tab
}

function terminalTab(fields: Partial<TerminalTab> = {}): Tab {
  return {
    type: TabType.TERMINAL,
    id: 't1',
    workspaceId: 'ws-1',
    position: 'b',
    workerId: 'worker-1',
    ...fields,
  } as Tab
}

/**
 * The two stores the rename reads, as spies. `RenameTabDeps` is narrowed to the
 * three methods the module calls, so these need no cast and a method the module
 * starts calling breaks the build here rather than silently going unasserted.
 */
function deps(tab?: Tab): RenameTabDeps & { patch: ReturnType<typeof vi.fn> } {
  const patch = vi.fn()
  return {
    patch,
    view: {
      getAgentTab: () => (tab?.type === TabType.AGENT ? tab as AgentTab : undefined),
      getTerminalTab: () => (tab?.type === TabType.TERMINAL ? tab as TerminalTab : undefined),
    },
    metadata: { patch },
  }
}

describe('renameTab', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('sends an agent title the worker will accept unchanged, and patches the same one', () => {
    const tab = agentTab()
    const d = deps(tab)

    renameTab(d, tab, '  Refactor the $parser"  ')

    expect(d.patch).toHaveBeenCalledWith('a1', { title: 'Refactor the parser' })
    expect(mockRenameAgent).toHaveBeenCalledWith('worker-1', { agentId: 'a1', title: 'Refactor the parser' })
  })

  // 50 CJK characters is 50 characters and 150 BYTES. The worker cuts it to the
  // 42 characters that fit in 128 bytes, so the optimistic patch has to hold
  // the same 42 -- otherwise the tab strip shows a title the worker never
  // stored, with no error anywhere.
  it('cuts an over-long CJK title to the byte limit before sending it', () => {
    const tab = agentTab()
    const d = deps(tab)

    renameTab(d, tab, '一'.repeat(50))

    const want = '一'.repeat(42)
    expect(d.patch).toHaveBeenCalledWith('a1', { title: want })
    expect(mockRenameAgent).toHaveBeenCalledWith('worker-1', { agentId: 'a1', title: want })
  })

  it('strips a control character', () => {
    const tab = agentTab()
    const d = deps(tab)

    renameTab(d, tab, 'Hello\u0000World')

    expect(mockRenameAgent).toHaveBeenCalledWith('worker-1', { agentId: 'a1', title: 'HelloWorld' })
  })

  // The sidebar tree used to patch the metadata and stop there, so a terminal
  // renamed from the sidebar lost its name on the next reload and reached no
  // other client.
  it('persists a terminal rename through UpdateTerminalTitle', () => {
    const tab = terminalTab({ ptyTitle: 'zsh' })
    const d = deps(tab)

    renameTab(d, tab, 'Build watcher')

    expect(mockUpdateTerminalTitle).toHaveBeenCalledWith('worker-1', { terminalId: 't1', title: 'Build watcher' })
  })

  it('clears ptyTitle for a terminal so the manual rename sticks', () => {
    const tab = terminalTab({ ptyTitle: 'zsh' })
    const d = deps(tab)

    renameTab(d, tab, 'Build watcher')

    expect(d.patch).toHaveBeenCalledWith('t1', { title: 'Build watcher', ptyTitle: '' })
  })

  it('does nothing when cleaning empties the title', () => {
    const tab = agentTab({ title: 'Agent Olivia' })
    const d = deps(tab)

    renameTab(d, tab, '  $$%%  ')

    expect(d.patch).not.toHaveBeenCalled()
    expect(mockRenameAgent).not.toHaveBeenCalled()
  })

  it('does nothing when the cleaned title equals the label the tab shows', () => {
    // The user opened the editor, typed a character the rule strips, and
    // committed. The cleaned title matches the current one, so the rename is a
    // no-op -- no RPC and no redundant broadcast to the owner's other clients.
    const tab = agentTab({ title: 'Agent Olivia' })
    const d = deps(tab)

    renameTab(d, tab, 'Agent Olivia$')

    expect(d.patch).not.toHaveBeenCalled()
    expect(mockRenameAgent).not.toHaveBeenCalled()
  })

  it('renames a terminal that shows its PTY title, because the label is not the stored title', () => {
    const tab = terminalTab({ ptyTitle: 'zsh' })
    const d = deps(tab)

    renameTab(d, tab, 'zsh II')

    expect(mockUpdateTerminalTitle).toHaveBeenCalledWith('worker-1', { terminalId: 't1', title: 'zsh II' })
  })

  it('leaves a FILE tab alone, because its title is its path', () => {
    const tab = { type: TabType.FILE, id: 'f1', workspaceId: 'ws-1', position: 'c', filePath: '/tmp/x.ts' } as Tab
    const d = deps()

    renameTab(d, tab, 'Something else')

    expect(d.patch).not.toHaveBeenCalled()
    expect(mockRenameAgent).not.toHaveBeenCalled()
    expect(mockUpdateTerminalTitle).not.toHaveBeenCalled()
  })

  it('sends an empty worker id when the tab lookup misses, rather than skipping the call', () => {
    const tab = agentTab()
    const d = deps() // no tab in the view

    renameTab(d, tab, 'Renamed')

    expect(mockRenameAgent).toHaveBeenCalledWith('', { agentId: 'a1', title: 'Renamed' })
  })

  it('warns when the agent rename fails', async () => {
    mockRenameAgent.mockRejectedValueOnce(new Error('offline'))
    const tab = agentTab()

    renameTab(deps(tab), tab, 'Renamed')
    await Promise.resolve()

    expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to rename agent', expect.any(Error))
  })

  it('warns when the terminal rename fails', async () => {
    mockUpdateTerminalTitle.mockRejectedValueOnce(new Error('offline'))
    const tab = terminalTab()

    renameTab(deps(tab), tab, 'Renamed')
    await Promise.resolve()

    expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to rename terminal', expect.any(Error))
  })
})
