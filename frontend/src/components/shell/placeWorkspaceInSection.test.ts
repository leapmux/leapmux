/// <reference types="vitest/globals" />
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { sectionClient } from '~/api/clients'
import { showWarnToast } from '~/components/common/Toast'
import { placeWorkspaceInSection } from './placeWorkspaceInSection'

vi.mock('~/api/clients', () => ({
  sectionClient: { moveWorkspace: vi.fn() },
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
  showWarnToast: vi.fn(),
  showErrorToast: vi.fn(),
}))

function makeDeps(items: { position: string }[] = []) {
  return {
    sectionStore: { getItemsForSection: vi.fn(() => items) },
    loadWorkspaces: vi.fn().mockResolvedValue(undefined),
  }
}

/** Lets a test resolve or reject the in-flight move RPC by hand. */
function deferredMove() {
  let settle!: (err?: Error) => void
  vi.mocked(sectionClient.moveWorkspace).mockReturnValue(
    new Promise((resolve, reject) => {
      settle = err => (err ? reject(err) : resolve({} as never))
    }) as never,
  )
  return () => settle
}

describe('placeWorkspaceInSection', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(sectionClient.moveWorkspace).mockResolvedValue({} as never)
  })

  it('refreshes without an RPC when there is no target section', async () => {
    // The shortcut path: no section was preselected, so the workspace stays
    // where CreateWorkspace put it and there is nothing to move.
    const deps = makeDeps()
    placeWorkspaceInSection(deps, 'ws-1', null)

    expect(sectionClient.moveWorkspace).not.toHaveBeenCalled()
    expect(deps.loadWorkspaces).toHaveBeenCalledTimes(1)
  })

  it('appends past the section\'s last item so the rank cannot collide', async () => {
    const deps = makeDeps([{ position: 'n' }, { position: 'u' }])
    placeWorkspaceInSection(deps, 'ws-1', 'sec-1')

    await vi.waitFor(() => expect(deps.loadWorkspaces).toHaveBeenCalled())
    const arg = vi.mocked(sectionClient.moveWorkspace).mock.calls[0][0]
    expect(arg).toMatchObject({ workspaceId: 'ws-1', sectionId: 'sec-1' })
    expect((arg.position ?? '') > 'u', 'the new rank must sort after the last item').toBe(true)
  })

  it('gives an empty section a valid rank rather than an empty string', async () => {
    const deps = makeDeps([])
    placeWorkspaceInSection(deps, 'ws-1', 'sec-1')

    await vi.waitFor(() => expect(deps.loadWorkspaces).toHaveBeenCalled())
    expect(vi.mocked(sectionClient.moveWorkspace).mock.calls[0][0].position).not.toBe('')
  })

  it('warns when the move fails, and still refreshes', async () => {
    // Regression: this call used to swallow its rejection with
    // `.catch(() => {})`, so a workspace could land in the default section
    // instead of the one the user clicked "+" in, with nothing on screen to
    // say so. Its two sibling copies in useWorkspaceOperations both warn.
    const settle = deferredMove()
    const deps = makeDeps([])
    placeWorkspaceInSection(deps, 'ws-1', 'sec-1')
    settle()(new Error('move failed'))

    await vi.waitFor(() => expect(showWarnToast).toHaveBeenCalledTimes(1))
    expect(showWarnToast).toHaveBeenCalledWith(
      'Failed to move the workspace into its section',
      expect.any(Error),
    )
    expect(deps.loadWorkspaces).toHaveBeenCalledTimes(1)
  })

  it('does not warn when the move succeeds', async () => {
    const deps = makeDeps([])
    placeWorkspaceInSection(deps, 'ws-1', 'sec-1')

    await vi.waitFor(() => expect(deps.loadWorkspaces).toHaveBeenCalled())
    expect(showWarnToast).not.toHaveBeenCalled()
  })
})
