/// <reference types="vitest/globals" />
import type { GitBranchEntry } from '~/generated/proto/leapmux/v1/git_pb'
import type { GitInfoFields, GitPathInfo } from '~/hooks/useGitPathInfo'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { generateSlug } from 'random-word-slugs'
import { createSignal, untrack } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { workerGit } from '~/api/workerRpc'
import { GitMode } from '~/hooks/useGitModeState'
import { deferred, settle } from '~/test-support/async'
import { GitOptions, indexBranches } from './GitOptions'

function existingNames(branches: readonly GitBranchEntry[]): Set<string> {
  return indexBranches(branches).existingNames
}

function entry(name: string, isRemote = false): GitBranchEntry {
  return { $typeName: 'leapmux.v1.GitBranchEntry', name, isRemote } as GitBranchEntry
}

describe('indexBranches.existingNames', () => {
  // The set avoids a branch scan on every keystroke.
  // Preserve the suffix rules from gitutil.StripRemotePrefix and stripRemotePrefix: remove exactly one leading remote segment.

  it('returns an empty set for an empty input', () => {
    expect(existingNames([])).toEqual(new Set())
  })

  it('local branches contribute only their exact name', () => {
    // A local branch such as feature/foo must not add foo. Only remote branches lose their first segment.
    const set = existingNames([
      entry('main'),
      entry('feature/foo'),
    ])
    expect([...set].toSorted()).toEqual(['feature/foo', 'main'])
  })

  it('remote branches contribute the full ref + the single first-segment strip', () => {
    // origin/feature/foo adds itself and feature/foo. It must not add foo.
    // The worker removes only one segment, so foo cannot conflict through that conversion.
    // Adding foo would incorrectly reject valid branch names in deeper remote namespaces.
    const set = existingNames([
      entry('origin/feature/foo', true),
    ])
    expect([...set].toSorted()).toEqual(['feature/foo', 'origin/feature/foo'])
    expect(set.has('foo')).toBe(false)
  })

  it('shallow remote refs add only the post-prefix name', () => {
    // "origin/foo" → adds "origin/foo" and "foo" (single segment strip).
    const set = existingNames([
      entry('origin/foo', true),
    ])
    expect([...set].toSorted()).toEqual(['foo', 'origin/foo'])
  })

  it('merges local and remote contributions into one collision set', () => {
    // A remote origin/foo must add itself and foo even when a local foo already exists.
    const set = existingNames([
      entry('foo'),
      entry('origin/foo', true),
    ])
    expect([...set].toSorted()).toEqual(['foo', 'origin/foo'])
  })

  it('local branch with a slash does NOT cause suffix-based collisions', () => {
    // Regression guard: only remote branches participate in the suffix
    // strip. A local "feature/foo" must not flag "foo".
    const set = existingNames([
      entry('feature/foo'),
    ])
    expect(set.has('feature/foo')).toBe(true)
    expect(set.has('foo')).toBe(false)
  })

  it('handles consecutive slashes in a malformed remote ref by stripping only the first segment', () => {
    // A malformed branch reference can contain //. Remove only one segment even for this invalid input.
    const set = existingNames([
      entry('origin//x', true),
    ])
    expect(set.has('origin//x')).toBe(true)
    expect(set.has('/x')).toBe(true)
    expect(set.has('x')).toBe(false)
  })

  it('a trailing slash adds the empty suffix (best-effort, harmless in practice)', () => {
    // validateBranchName rejects empty input before this lookup, so an empty entry cannot cause a false conflict.
    // Keep this case to prevent an empty-string special case from removing valid suffixes.
    const set = existingNames([
      entry('origin/', true),
    ])
    expect(set.has('origin/')).toBe(true)
    expect(set.has('')).toBe(true)
  })

  it('is idempotent on duplicate inputs', () => {
    // A refresh can supply duplicate entries. The set must count each branch only once.
    const set = existingNames([
      entry('main'),
      entry('main'),
      entry('origin/main', true),
      entry('origin/main', true),
    ])
    expect([...set].toSorted()).toEqual(['main', 'origin/main'])
  })

  it('does not match across branches (single-segment strip is per-ref)', () => {
    // Each remote branch contributes its own first-segment removal. Do not derive deeper suffixes from other references.
    const set = existingNames([
      entry('origin/a/foo', true),
      entry('upstream/b/foo', true),
    ])
    // "origin/a/foo" → "a/foo"; "upstream/b/foo" → "b/foo".
    expect(set.has('a/foo')).toBe(true)
    expect(set.has('b/foo')).toBe(true)
    // A deep remote suffix alone must not mark the local name foo as a conflict.
    expect(set.has('foo')).toBe(false)
    expect(set.has('origin/b/foo')).toBe(false)
  })
})

describe('indexBranches', () => {
  // indexBranches computes these results in one pass:
  // - The local and remote branch lists.
  // - The set of existing branch names.
  // - The local names used to warn about remote conflicts.
  // Check every result to prevent a partial optimization.

  it('returns empty sets and empty arrays for empty input', () => {
    const idx = indexBranches([])
    expect(idx.local).toEqual([])
    expect(idx.remote).toEqual([])
    expect(idx.localNames).toEqual(new Set())
    expect(idx.existingNames).toEqual(new Set())
  })

  it('preserves input order within each partition', () => {
    // The branch menu retains input order. Check both lists so a Set or Map refactor cannot reorder the UI.
    const idx = indexBranches([
      entry('z'),
      entry('a'),
      entry('m'),
      entry('origin/z', true),
      entry('origin/a', true),
    ])
    expect(idx.local.map(b => b.name)).toEqual(['z', 'a', 'm'])
    expect(idx.remote.map(b => b.name)).toEqual(['origin/z', 'origin/a'])
  })

  it('reports localNames as the set of local branch names only', () => {
    const idx = indexBranches([
      entry('main'),
      entry('feature/foo'),
      entry('origin/main', true),
      entry('origin/main', true),
    ])
    expect([...idx.localNames].toSorted()).toEqual(['feature/foo', 'main'])
  })

  it('partition + localNames coexist with existingNames in one pass', () => {
    // All outputs must agree:
    // - Every localNames entry also belongs to existingNames.
    // - Every local entry has isRemote=false.
    // - Every remote entry has isRemote=true.
    const idx = indexBranches([
      entry('main'),
      entry('feature/x'),
      entry('origin/main', true),
      entry('origin/feature/y', true),
    ])
    for (const name of idx.localNames)
      expect(idx.existingNames.has(name)).toBe(true)
    for (const b of idx.local)
      expect(b.isRemote).toBe(false)
    for (const b of idx.remote)
      expect(b.isRemote).toBe(true)
  })
})

// gitMode supplies the initial selection only. Later changes leave through onGitModeChange.
// Dialog callers do not change gitMode independently. These cases verify that later prop changes cannot override the internal selection.
// ChangeBranchDialog tests cover clicks, but cannot observe that initial-only prop read.

vi.mock('random-word-slugs', () => ({
  generateSlug: vi.fn(() => 'seeded-branch-name'),
}))

vi.mock('~/api/workerRpc', () => ({
  workerGit: {
    getGitWorkingTreeState: vi.fn(),
    listGitBranches: vi.fn(),
    listGitWorktrees: vi.fn(),
  },
}))

function makeGitInfo(overrides: Partial<GitInfoFields> = {}): GitPathInfo {
  const info: GitInfoFields = {
    isGitRepo: true,
    isRepoRoot: true,
    isWorktreeRoot: false,
    repoRoot: '/repo',
    repoDirName: 'repo',
    currentBranch: 'main',
    attachedBranch: overrides.currentBranch ?? 'main',
    errorHint: '',
    ...overrides,
  }
  return {
    loading: () => false,
    info: () => info,
    showGitOptions: () => true,
  }
}

type WorkingTreeResponse = Awaited<ReturnType<typeof workerGit.getGitWorkingTreeState>>

function workingTreeResponse(isDirty: boolean): WorkingTreeResponse {
  return { $typeName: 'leapmux.v1.GetGitWorkingTreeStateResponse', isDirty }
}

function prepareGitMocks(): void {
  vi.clearAllMocks()
  vi.mocked(workerGit.getGitWorkingTreeState).mockResolvedValue({
    $typeName: 'leapmux.v1.GetGitWorkingTreeStateResponse',
    isDirty: false,
  })
  vi.mocked(workerGit.listGitBranches).mockResolvedValue({
    $typeName: 'leapmux.v1.ListGitBranchesResponse',
    branches: [],
    currentBranch: 'main',
    attachedBranch: 'main',
  })
  vi.mocked(workerGit.listGitWorktrees).mockResolvedValue({
    $typeName: 'leapmux.v1.ListGitWorktreesResponse',
    worktrees: [],
  })
}

function renderGitOptions(overrides: Partial<Parameters<typeof GitOptions>[0]> = {}) {
  return render(() => (
    <GitOptions
      workerId="w1"
      selectedPath="/repo"
      gitInfo={makeGitInfo()}
      gitMode={() => GitMode.Current}
      onGitModeChange={vi.fn()}
      {...overrides}
    />
  ))
}

describe('GitOptions activeMode ownership', () => {
  beforeEach(prepareGitMocks)

  it('reads no working tree in current mode or use worktree mode', async () => {
    renderGitOptions()
    await settle()
    expect(workerGit.getGitWorkingTreeState).not.toHaveBeenCalled()
    fireEvent.click(screen.getByLabelText('Use existing worktree'))
    await settle()
    expect(workerGit.getGitWorkingTreeState).not.toHaveBeenCalled()
  })

  it('checks the working tree after selecting a warning mode', async () => {
    vi.mocked(workerGit.getGitWorkingTreeState).mockResolvedValue({
      $typeName: 'leapmux.v1.GetGitWorkingTreeStateResponse',
      isDirty: true,
    })
    renderGitOptions()
    fireEvent.click(screen.getByLabelText('Switch to branch'))
    await waitFor(() => expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible())
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledExactlyOnceWith('w1', { path: '/repo' }, {
      signal: expect.any(AbortSignal),
    })
  })

  it('checks the working tree when mounted in switch branch mode', async () => {
    vi.mocked(workerGit.getGitWorkingTreeState).mockResolvedValue({
      $typeName: 'leapmux.v1.GetGitWorkingTreeStateResponse',
      isDirty: true,
    })
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
    })
    await waitFor(() => expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible())
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledExactlyOnceWith('w1', { path: '/repo' }, {
      signal: expect.any(AbortSignal),
    })
  })

  it('drops a working-tree answer that arrives after a folder change', async () => {
    const old = deferred<WorkingTreeResponse>()
    vi.mocked(workerGit.getGitWorkingTreeState).mockReturnValueOnce(old.promise)
    const [path, setPath] = createSignal('/repo-a')
    renderGitOptions({
      get selectedPath() {
        return path()
      },
    })
    fireEvent.click(screen.getByLabelText('Switch to branch'))
    await waitFor(() => expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1))
    setPath('/repo-b')
    old.resolve(workingTreeResponse(true))
    await settle()
    fireEvent.click(screen.getByLabelText('Switch to branch'))
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
    expect(workerGit.getGitWorkingTreeState).toHaveBeenLastCalledWith('w1', { path: '/repo-b' }, {
      signal: expect.any(AbortSignal),
    })
    expect(screen.queryByText(/Switching branches may fail or discard changes/)).not.toBeInTheDocument()
  })

  it('refetches working tree state when refreshing a warning mode', async () => {
    const [refresh, setRefresh] = createSignal(0)
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
      get refreshKey() {
        return refresh()
      },
    })
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    vi.mocked(workerGit.getGitWorkingTreeState).mockResolvedValue({
      $typeName: 'leapmux.v1.GetGitWorkingTreeStateResponse',
      isDirty: true,
    })
    setRefresh(1)
    await waitFor(() => expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible())
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
  })

  it.each([
    [GitMode.SwitchBranch, 'Switching branches may fail or discard changes'],
    [GitMode.CreateBranch, 'Creating a new branch will include them'],
    [GitMode.CreateWorktree, 'will not be transferred to the new worktree'],
  ] as const)('shows an unavailable status in %s without an automatic retry', async (mode, dirtyCopy) => {
    vi.mocked(workerGit.getGitWorkingTreeState).mockRejectedValue(new Error('offline'))
    const onGitModeChange = vi.fn()
    renderGitOptions({
      gitMode: () => mode,
      onGitModeChange,
    })
    await waitFor(() => expect(screen.getByText('Working-tree status is unavailable.')).toBeVisible())
    expect(screen.getByRole('button', { name: 'Retry' })).toBeEnabled()
    expect(screen.queryByText(new RegExp(dirtyCopy))).not.toBeInTheDocument()
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining({ mode }))
    fireEvent.click(screen.getByLabelText('Use current state'))
    await settle()
    expect(screen.queryByText('Working-tree status is unavailable.')).not.toBeInTheDocument()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
  })

  it.each([false, true])('retries unavailable status and displays the recovered dirty value %s', async (isDirty) => {
    vi.mocked(workerGit.getGitWorkingTreeState).mockRejectedValueOnce(new Error('offline'))
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
    })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry' })).toBeVisible())
    const retry = deferred<WorkingTreeResponse>()
    vi.mocked(workerGit.getGitWorkingTreeState).mockReturnValueOnce(retry.promise)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(screen.getByRole('button', { name: 'Retry' })).toBeDisabled()
    retry.resolve(workingTreeResponse(isDirty))
    await waitFor(() => expect(screen.queryByText('Working-tree status is unavailable.')).not.toBeInTheDocument())
    expect(Boolean(screen.queryByText(/Switching branches may fail or discard changes/))).toBe(isDirty)
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
  })

  it('replaces a known dirty warning with unavailable status when refresh fails', async () => {
    vi.mocked(workerGit.getGitWorkingTreeState).mockResolvedValueOnce(workingTreeResponse(true))
    const [refresh, setRefresh] = createSignal(0)
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
      get refreshKey() { return refresh() },
    })
    await waitFor(() => expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible())
    vi.mocked(workerGit.getGitWorkingTreeState).mockRejectedValueOnce(new Error('index unavailable'))
    setRefresh(1)
    await waitFor(() => expect(screen.getByText('Working-tree status is unavailable.')).toBeVisible())
    expect(screen.queryByText(/Switching branches may fail or discard changes/)).not.toBeInTheDocument()
  })

  it('ignores a stale failure after a newer successful refresh', async () => {
    const old = deferred<WorkingTreeResponse>()
    vi.mocked(workerGit.getGitWorkingTreeState).mockReturnValueOnce(old.promise)
    const [refresh, setRefresh] = createSignal(0)
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
      get refreshKey() { return refresh() },
    })
    await settle()
    vi.mocked(workerGit.getGitWorkingTreeState).mockResolvedValueOnce(workingTreeResponse(true))
    setRefresh(1)
    await waitFor(() => expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible())
    old.reject(new Error('stale failure'))
    await settle()
    expect(screen.queryByText('Working-tree status is unavailable.')).not.toBeInTheDocument()
    expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible()
  })

  it('checks again on refresh after a working tree request fails', async () => {
    vi.mocked(workerGit.getGitWorkingTreeState).mockRejectedValueOnce(new Error('offline'))
    const [refresh, setRefresh] = createSignal(0)
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
      get refreshKey() {
        return refresh()
      },
    })
    await settle()
    expect(screen.queryByText(/Switching branches may fail or discard changes/)).not.toBeInTheDocument()
    vi.mocked(workerGit.getGitWorkingTreeState).mockResolvedValue({
      $typeName: 'leapmux.v1.GetGitWorkingTreeStateResponse',
      isDirty: true,
    })
    setRefresh(1)
    await waitFor(() => expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible())
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
  })

  it.each(['path', 'worker'] as const)('refetches a restricted warning mode after a %s reset with preloaded branches', async (target) => {
    vi.mocked(workerGit.getGitWorkingTreeState).mockImplementation(async (workerId, { path }) => ({
      $typeName: 'leapmux.v1.GetGitWorkingTreeStateResponse',
      isDirty: workerId === 'w2' || path === '/repo-b',
    }))
    const [path, setPath] = createSignal('/repo-a')
    const [workerId, setWorkerId] = createSignal('w1')
    renderGitOptions({
      get workerId() {
        return workerId()
      },
      get selectedPath() {
        return path()
      },
      gitMode: () => GitMode.SwitchBranch,
      modes: [GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree],
      preloadedBranches: () => [
        entry('main'),
      ],
    })
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    expect(screen.queryByText(/Switching branches may fail or discard changes/)).not.toBeInTheDocument()

    if (target === 'path')
      setPath('/repo-b')
    else
      setWorkerId('w2')
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
    expect(workerGit.getGitWorkingTreeState).toHaveBeenLastCalledWith(untrack(workerId), {
      path: untrack(path),
    }, {
      signal: expect.any(AbortSignal),
    })
    expect(screen.getByLabelText('Switch to branch')).toBeChecked()
    expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible()
    expect(workerGit.listGitBranches).not.toHaveBeenCalled()
  })

  it.each(['path', 'worker'] as const)('replaces a pending restricted observation after a %s reset', async (target) => {
    const old = deferred<WorkingTreeResponse>()
    const latest = deferred<WorkingTreeResponse>()
    vi.mocked(workerGit.getGitWorkingTreeState).mockImplementation((workerId, { path }) =>
      workerId === 'w1' && path === '/repo-a'
        ? old.promise
        : latest.promise)
    const [path, setPath] = createSignal('/repo-a')
    const [workerId, setWorkerId] = createSignal('w1')
    renderGitOptions({
      get workerId() {
        return workerId()
      },
      get selectedPath() {
        return path()
      },
      gitMode: () => GitMode.SwitchBranch,
      modes: [GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree],
      preloadedBranches: () => [
        entry('main'),
      ],
    })
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    if (target === 'path')
      setPath('/repo-b')
    else
      setWorkerId('w2')
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
    expect(screen.queryByText(/Switching branches may fail or discard changes/)).not.toBeInTheDocument()
    latest.resolve(workingTreeResponse(true))
    await settle()
    expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible()
    old.resolve(workingTreeResponse(false))
    await settle()
    expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible()
  })

  it.each(['path', 'worker'] as const)('keeps a %s reset free of status reads until a warning mode is selected', async (target) => {
    const old = deferred<WorkingTreeResponse>()
    vi.mocked(workerGit.getGitWorkingTreeState).mockReturnValueOnce(old.promise)
    const [path, setPath] = createSignal('/repo-a')
    const [workerId, setWorkerId] = createSignal('w1')
    renderGitOptions({
      get workerId() {
        return workerId()
      },
      get selectedPath() {
        return path()
      },
      gitMode: () => GitMode.SwitchBranch,
    })
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    if (target === 'path')
      setPath('/repo-b')
    else
      setWorkerId('w2')
    await settle()
    expect(screen.getByLabelText('Use current state')).toBeChecked()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByLabelText('Use existing worktree'))
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByLabelText('Switch to branch'))
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
    expect(workerGit.getGitWorkingTreeState).toHaveBeenLastCalledWith(untrack(workerId), {
      path: untrack(path),
    }, {
      signal: expect.any(AbortSignal),
    })
    old.resolve(workingTreeResponse(true))
    await settle()
    expect(screen.queryByText(/Switching branches may fail or discard changes/)).not.toBeInTheDocument()
  })

  it('waits for valid targets and readiness before refetching a restricted warning mode', async () => {
    const [path, setPath] = createSignal('/repo')
    const [workerId, setWorkerId] = createSignal('w1')
    const [ready, setReady] = createSignal(false)
    renderGitOptions({
      get workerId() {
        return workerId()
      },
      get selectedPath() {
        return path()
      },
      gitInfo: {
        ...makeGitInfo(),
        showGitOptions: ready,
      },
      gitMode: () => GitMode.SwitchBranch,
      modes: [GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree],
      preloadedBranches: () => [
        entry('main'),
      ],
    })
    setPath('/repo-b')
    await settle()
    expect(workerGit.getGitWorkingTreeState).not.toHaveBeenCalled()
    setReady(true)
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    setPath('')
    setWorkerId('')
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    setPath('/repo-c')
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    setWorkerId('w2')
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
    expect(workerGit.getGitWorkingTreeState).toHaveBeenLastCalledWith('w2', { path: '/repo-c' }, {
      signal: expect.any(AbortSignal),
    })
  })

  it('shares a pending status observation across warning modes and caches its result', async () => {
    const pending = deferred<WorkingTreeResponse>()
    vi.mocked(workerGit.getGitWorkingTreeState).mockReturnValueOnce(pending.promise)
    renderGitOptions()
    for (const label of [
      'Switch to branch',
      'Create new branch',
      'Create new worktree',
      'Use current state',
      'Switch to branch',
    ]) {
      fireEvent.click(screen.getByLabelText(label))
    }
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    pending.resolve(workingTreeResponse(true))
    await settle()
    expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible()
    fireEvent.click(screen.getByLabelText('Create new worktree'))
    await settle()
    expect(screen.getByText(/will not be transferred to the new worktree/)).toBeVisible()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
  })

  it('replaces a pending observation on refresh and ignores the older response', async () => {
    const old = deferred<WorkingTreeResponse>()
    const latest = deferred<WorkingTreeResponse>()
    let calls = 0
    vi.mocked(workerGit.getGitWorkingTreeState).mockImplementation(() => ++calls === 1
      ? old.promise
      : latest.promise)
    const [refresh, setRefresh] = createSignal(0)
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
      get refreshKey() {
        return refresh()
      },
    })
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    setRefresh(1)
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
    latest.resolve(workingTreeResponse(true))
    await settle()
    expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible()
    old.resolve(workingTreeResponse(false))
    await settle()
    expect(screen.getByText(/Switching branches may fail or discard changes/)).toBeVisible()
  })

  it('retries a failed observation only after selecting another warning mode', async () => {
    vi.mocked(workerGit.getGitWorkingTreeState).mockRejectedValueOnce(new Error('offline'))
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
    })
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByLabelText('Create new branch'))
    await settle()
    expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
    expect(screen.queryByText(/Creating a new branch will include them/)).not.toBeInTheDocument()
  })

  it.each(['path', 'worker', 'refresh', 'unmount'] as const)('aborts the pending client observation on %s', async (action) => {
    const pending = deferred<WorkingTreeResponse>()
    const rejectedOnAbort = vi.fn()
    vi.mocked(workerGit.getGitWorkingTreeState).mockImplementationOnce((_workerId, _request, options) => {
      const signal = options?.signal
      signal?.addEventListener('abort', () => {
        rejectedOnAbort()
        pending.reject(signal.reason)
      }, { once: true })
      return pending.promise
    })
    const [path, setPath] = createSignal('/repo-a')
    const [workerId, setWorkerId] = createSignal('w1')
    const [refresh, setRefresh] = createSignal(0)
    const view = renderGitOptions({
      get workerId() {
        return workerId()
      },
      get selectedPath() {
        return path()
      },
      get refreshKey() {
        return refresh()
      },
      gitMode: () => GitMode.SwitchBranch,
    })
    await settle()
    const firstSignal = vi.mocked(workerGit.getGitWorkingTreeState).mock.calls[0]?.[2]?.signal
    expect(firstSignal).toBeInstanceOf(AbortSignal)
    expect(firstSignal?.aborted).toBe(false)
    if (action === 'path')
      setPath('/repo-b')
    else if (action === 'worker')
      setWorkerId('w2')
    else if (action === 'refresh')
      setRefresh(1)
    else
      view.unmount()
    await settle()
    expect(firstSignal?.aborted).toBe(true)
    expect(rejectedOnAbort).toHaveBeenCalledTimes(1)
    if (action === 'path' || action === 'worker') {
      expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(1)
      fireEvent.click(screen.getByLabelText('Switch to branch'))
      await settle()
    }
    if (action !== 'unmount') {
      expect(workerGit.getGitWorkingTreeState).toHaveBeenCalledTimes(2)
      const latestSignal = vi.mocked(workerGit.getGitWorkingTreeState).mock.calls[1]?.[2]?.signal
      expect(latestSignal).toBeInstanceOf(AbortSignal)
      expect(latestSignal).not.toBe(firstSignal)
      expect(latestSignal?.aborted).toBe(false)
    }
  })

  it('seeds the radio from `props.gitMode()` at mount and ignores subsequent external mutations', async () => {
    // Read gitMode once with untrack to initialize activeMode. Later prop updates must not replace the internal mode.
    const [mode, setMode] = createSignal<GitMode>(GitMode.SwitchBranch)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
    })

    await waitFor(() => expect(screen.getByLabelText('Switch to branch')).toBeChecked())

    setMode(GitMode.CreateBranch)
    // SolidJS flushes within a single microtask; two hops is enough
    // headroom to catch any reactive echo before asserting the radio
    // stayed put.
    await Promise.resolve()
    await Promise.resolve()

    expect(screen.getByLabelText('Switch to branch')).toBeChecked()
    expect(screen.getByLabelText('Create new branch')).not.toBeChecked()
  })

  it('radio clicks update the internal active mode AND emit an intent (uni-directional)', async () => {
    // The radio calls setRawMode. activeMode recomputes, and the emit effect
    // builds the intent with intentFor and calls onGitModeChange. Assert the
    // radio selection and emitted intent so the parent's mode stays synchronized.
    const [mode] = createSignal<GitMode>(GitMode.SwitchBranch)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
    })

    await waitFor(() => expect(screen.getByLabelText('Switch to branch')).toBeChecked())
    onGitModeChange.mockClear()

    fireEvent.click(screen.getByLabelText('Create new branch'))

    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())
    expect(screen.getByLabelText('Switch to branch')).not.toBeChecked()

    // The emit runs through a reactive effect. Wait for the assertion before
    // checking its calls.
    await waitFor(() => {
      const intents = onGitModeChange.mock.calls.map(c => (c[0] as { mode: GitMode }).mode)
      expect(intents).toContain(GitMode.CreateBranch)
    })
  })

  it('emits an initial intent on mount derived from the seed mode', async () => {
    // The createEffect runs synchronously after mount and emits the
    // intent built from `intentFor(activeMode())`, so a dialog opened
    // with a non-default seed sees the parent gitMode track the seed
    // immediately — no "first user action required" lag.
    const [mode] = createSignal<GitMode>(GitMode.CreateWorktree)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
      modes: [GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree],
    })

    await waitFor(() => expect(onGitModeChange).toHaveBeenCalled())
    const firstIntent = onGitModeChange.mock.calls[0]?.[0] as { mode: GitMode }
    expect(firstIntent.mode).toBe(GitMode.CreateWorktree)
  })

  it('falls back to DEFAULT_GIT_MODES when modes prop is the empty array', async () => {
    // `props.modes ?? DEFAULT_GIT_MODES` only triggers on null/undefined,
    // so an empty-array prop used to make defaultMode() return undefined
    // and the activeMode signal hold a non-GitMode value. The guard
    // treats an empty array as "no modes specified" and falls back to
    // DEFAULT_GIT_MODES, so the radio still has a coherent initial state.
    const [mode] = createSignal<GitMode>(GitMode.SwitchBranch)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
      modes: [],
    })

    // DEFAULT_GIT_MODES includes Current as the first/default mode, so
    // the seed intent emitted on mount must NOT be undefined.
    await waitFor(() => expect(onGitModeChange).toHaveBeenCalled())
    const firstIntent = onGitModeChange.mock.calls[0]?.[0] as { mode: GitMode | undefined }
    expect(firstIntent.mode).toBeDefined()
    // And the radio is rendered (proves the fallback list reached the
    // render loop). Use a generic label that DEFAULT_GIT_MODES always
    // includes.
    expect(screen.getByLabelText('Use current state')).toBeInTheDocument()
  })

  it('clamps a seed mode that is not in the enabled set', async () => {
    // A seed outside `props.modes` leaves EVERY radio unchecked -- each row is
    // `enabledModes().has` -- while the emit effect still reports that mode, so
    // the dialog submits an intent it never showed. The remembered
    // per-repository mode makes that reachable: a mode stored from a dialog
    // that offers all five can arrive at one that offers three.
    const [mode] = createSignal<GitMode>(GitMode.UseWorktree)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
      modes: [GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree],
    })

    // The first enabled mode, because Current is not enabled either.
    await waitFor(() => expect(screen.getByLabelText('Switch to branch')).toBeChecked())
    await waitFor(() => expect(onGitModeChange).toHaveBeenCalled())
    const firstIntent = onGitModeChange.mock.calls[0]?.[0] as { mode: GitMode }
    expect(firstIntent.mode).toBe(GitMode.SwitchBranch)
  })

  it('clamps to Current when the enabled set includes it', async () => {
    const [mode] = createSignal<GitMode>(GitMode.UseWorktree)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
      modes: [GitMode.Current, GitMode.SwitchBranch],
    })

    await waitFor(() => expect(screen.getByLabelText('Use current state')).toBeChecked())
    expect(screen.getByLabelText('Switch to branch')).not.toBeChecked()
  })

  it('passes a seed that IS in the enabled set through unchanged', async () => {
    // The guard rail on the clamp above: a legitimate seed must not be
    // rewritten to the default.
    const [mode] = createSignal<GitMode>(GitMode.CreateWorktree)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
      modes: [GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree],
    })

    await waitFor(() => expect(screen.getByLabelText('Create new worktree')).toBeChecked())
  })

  // The active mode is DERIVED from the enabled set, not repaired when the set
  // changes. A narrowing that still CONTAINS the user's pick must therefore
  // keep it -- the old reset effect wiped it, along with the cached branch
  // list, the worktrees and all three selections.
  it('keeps a still-valid pick when the enabled set narrows', async () => {
    const [mode] = createSignal<GitMode>(GitMode.Current)
    const [modes, setModes] = createSignal<GitMode[]>([
      GitMode.Current,
      GitMode.SwitchBranch,
      GitMode.CreateBranch,
    ])

    renderGitOptions({
      gitMode: mode,
      get modes() {
        return modes()
      },
    })

    await waitFor(() => expect(screen.getByLabelText('Use current state')).toBeChecked())
    // CreateBranch, not SwitchBranch: the narrowed set's DEFAULT is
    // SwitchBranch, so picking that would pass either way.
    fireEvent.click(screen.getByLabelText('Create new branch'))
    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())

    setModes([GitMode.SwitchBranch, GitMode.CreateBranch])

    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())
    expect(screen.getByLabelText('Switch to branch')).not.toBeChecked()
  })

  // The other half: a narrowing that EXCLUDES the pick re-derives to the
  // default rather than leaving every radio unchecked while the hidden mode
  // keeps emitting.
  it('re-derives to the default when the enabled set drops the pick', async () => {
    const [mode] = createSignal<GitMode>(GitMode.Current)
    const [modes, setModes] = createSignal<GitMode[]>([GitMode.Current, GitMode.SwitchBranch])

    renderGitOptions({
      gitMode: mode,
      get modes() {
        return modes()
      },
    })

    await waitFor(() => expect(screen.getByLabelText('Use current state')).toBeChecked())

    setModes([GitMode.SwitchBranch, GitMode.CreateBranch])

    await waitFor(() => expect(screen.getByLabelText('Switch to branch')).toBeChecked())
  })

  it('honours a seed inside DEFAULT_GIT_MODES when modes is the empty array', async () => {
    // The second guard rail: an empty `modes` falls back to the default set,
    // so the clamp must judge the seed against THAT set and not refuse it.
    const [mode] = createSignal<GitMode>(GitMode.UseWorktree)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
      modes: [],
    })

    await waitFor(() => expect(screen.getByLabelText('Use existing worktree')).toBeChecked())
  })

  it('resets the mode to the default when the selected path changes', async () => {
    // A repository change invalidates the checkout branch, base branch, and worktree path. It also replaces their source lists.
    // Reset the mode also, so it cannot retain selections from the previous repository.
    const [mode] = createSignal<GitMode>(GitMode.Current)
    const [path, setPath] = createSignal('/repo')
    const onGitModeChange = vi.fn()

    renderGitOptions({
      get selectedPath() {
        return path()
      },
      gitMode: mode,
      onGitModeChange,
    })

    fireEvent.click(screen.getByLabelText('Create new branch'))
    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())
    onGitModeChange.mockClear()

    setPath('/other-repo')

    await waitFor(() => expect(screen.getByLabelText('Use current state')).toBeChecked())
    expect(screen.getByLabelText('Create new branch')).not.toBeChecked()
    // The parent is told, not left believing the old mode still applies.
    await waitFor(() => {
      const intents = onGitModeChange.mock.calls.map(c => (c[0] as { mode: GitMode }).mode)
      expect(intents).toContain(GitMode.Current)
    })
  })

  it('drops a branch list that arrives after a folder change', async () => {
    const old = deferred<Awaited<ReturnType<typeof workerGit.listGitBranches>>>()
    vi.mocked(workerGit.listGitBranches).mockReturnValueOnce(old.promise)
    const [path, setPath] = createSignal('/repo-a')
    renderGitOptions({
      get selectedPath() {
        return path()
      },
    })
    fireEvent.click(screen.getByLabelText('Switch to branch'))
    await waitFor(() => expect(workerGit.listGitBranches).toHaveBeenCalledTimes(1))
    setPath('/repo-b')
    old.resolve({
      $typeName: 'leapmux.v1.ListGitBranchesResponse',
      branches: [
        entry('old-branch'),
      ],
      currentBranch: 'main',
      attachedBranch: 'main',
    })
    await Promise.resolve()
    await Promise.resolve()
    fireEvent.click(screen.getByLabelText('Switch to branch'))
    await waitFor(() => expect(workerGit.listGitBranches).toHaveBeenCalledTimes(2))
    expect(workerGit.listGitBranches).toHaveBeenLastCalledWith('w1', { path: '/repo-b' })
    expect(screen.queryByText('old-branch')).not.toBeInTheDocument()
  })

  it('keeps the mode when the selected path is set to the same value', async () => {
    // The reset is keyed on the path CHANGING. A re-render that hands back
    // an identical path (a parent re-computing the same string) must not
    // throw away a mode the user just picked.
    const [mode] = createSignal<GitMode>(GitMode.Current)
    const [path, setPath] = createSignal('/repo')
    const onGitModeChange = vi.fn()

    renderGitOptions({
      get selectedPath() {
        return path()
      },
      gitMode: mode,
      onGitModeChange,
    })

    fireEvent.click(screen.getByLabelText('Create new branch'))
    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())

    setPath('/repo')
    await Promise.resolve()
    await Promise.resolve()

    expect(screen.getByLabelText('Create new branch')).toBeChecked()
  })
})

describe('GitOptions branch name field', () => {
  beforeEach(prepareGitMocks)

  // An empty branch field must remain empty and show its validation error.
  // A randomSlug fallback previously restored the generated value and made the empty-input path unreachable.
  // One signal now supplies the literal field value, as the NewWorkspace title field does.
  it('keeps the field empty and shows an error when the default name is cleared', async () => {
    const [mode] = createSignal<GitMode>(GitMode.CreateBranch)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
      modes: [GitMode.CreateBranch],
    })

    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())

    const input = screen.getByPlaceholderText('feature-branch') as HTMLInputElement
    // Seeded with a non-empty random slug.
    expect(input.value).not.toBe('')

    // Clear the field. Its value must remain empty instead of restoring the generated value.
    fireEvent.input(input, {
      target: { value: '' },
    })
    await Promise.resolve()
    await Promise.resolve()
    expect(input.value).toBe('')

    // An empty value must show the validateBranchName error below the field, as the NewWorkspace title field does.
    expect(screen.getByText('Branch name must not be empty')).toBeInTheDocument()
  })

  // Randomize replaces the field value.
  // After a user clears the field, Randomize must supply a new value and clear the empty-name error.
  it('replaces a populated name and restores a cleared name when randomize is clicked', async () => {
    vi.mocked(generateSlug)
      .mockReturnValueOnce('first-branch')
      .mockReturnValueOnce('second-branch')
      .mockReturnValueOnce('third-branch')
    const [mode] = createSignal<GitMode>(GitMode.CreateBranch)
    const onGitModeChange = vi.fn()

    renderGitOptions({
      gitMode: mode,
      onGitModeChange,
      modes: [GitMode.CreateBranch],
    })

    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())

    const input = screen.getByPlaceholderText('feature-branch') as HTMLInputElement
    expect(input.value).toBe('first-branch')
    fireEvent.click(screen.getByLabelText('Generate random name'))
    expect(input.value).toBe('second-branch')
    fireEvent.input(input, {
      target: { value: '' },
    })
    await Promise.resolve()
    await Promise.resolve()
    expect(input.value).toBe('')

    // Randomize must replace the empty value and clear its error.
    // Tooltip exposes the button description through aria-label.
    fireEvent.click(screen.getByLabelText('Generate random name'))
    await Promise.resolve()
    await Promise.resolve()
    expect(input.value).toBe('third-branch')
    expect(screen.queryByText('Branch name must not be empty')).not.toBeInTheDocument()
  })
})

describe('GitOptions attached branch identity', () => {
  beforeEach(prepareGitMocks)

  it.each([
    { preloaded: false, remote: false },
    { preloaded: true, remote: false },
    { preloaded: false, remote: true },
    { preloaded: true, remote: true },
  ])('permits detached same-label attachment with %j', async ({ preloaded, remote }) => {
    const label = 'a1b2c3d'
    const selected = remote
      ? `upstream/${label}`
      : label
    const branches = [
      entry(label),
      entry(`upstream/${label}`, true),
    ]
    vi.mocked(workerGit.listGitBranches).mockResolvedValue({
      $typeName: 'leapmux.v1.ListGitBranchesResponse',
      branches,
      currentBranch: label,
      attachedBranch: '',
    })
    const onGitModeChange = vi.fn()
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
      gitInfo: makeGitInfo({ currentBranch: label, attachedBranch: '' }),
      onGitModeChange,
      ...(preloaded
        ? {
            preloadedBranches: () => branches,
          }
        : {}),
    })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Branch' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Branch' }))
    expect(screen.queryByRole('menuitemradio', { name: `${label} (current)` })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('menuitemradio', { name: selected }))
    await waitFor(() => expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining({
      mode: GitMode.SwitchBranch,
      checkoutBranch: selected,
      checkoutBranchError: null,
    })))
  })

  it.each([GitMode.CreateBranch, GitMode.CreateWorktree])('uses HEAD as the detached default base in mode %s', async (mode) => {
    const label = 'a1b2c3d'
    const branches = [
      entry('main'),
      entry(label),
    ]
    vi.mocked(workerGit.listGitBranches).mockResolvedValue({
      $typeName: 'leapmux.v1.ListGitBranchesResponse',
      branches,
      currentBranch: label,
      attachedBranch: '',
    })
    const onGitModeChange = vi.fn()
    renderGitOptions({
      gitMode: () => mode,
      gitInfo: makeGitInfo({ currentBranch: label, attachedBranch: '' }),
      onGitModeChange,
    })
    await waitFor(() => expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining(
      mode === GitMode.CreateBranch
        ? { createBranchBase: '' }
        : { worktreeBaseBranch: '' },
    )))
    fireEvent.click(screen.getByRole('button', { name: 'Branch' }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: label }))
    await waitFor(() => expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining(
      mode === GitMode.CreateBranch
        ? { createBranchBase: label }
        : { worktreeBaseBranch: label },
    )))
  })

  it('clears an automatic stale attached base but preserves an explicit selection', async () => {
    const branches = [
      entry('main'),
      entry('other'),
    ]
    const [snapshot, setSnapshot] = createSignal(makeGitInfo({ attachedBranch: 'main' }).info())
    const onGitModeChange = vi.fn()
    renderGitOptions({
      gitMode: () => GitMode.CreateBranch,
      gitInfo: {
        info: snapshot,
        loading: () => false,
        showGitOptions: () => true,
      },
      preloadedBranches: () => branches,
      onGitModeChange,
    })
    await waitFor(() => expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining({ createBranchBase: 'main' })))
    setSnapshot(current => ({
      ...current,
      currentBranch: 'a1b2c3d',
      attachedBranch: '',
    }))
    await waitFor(() => expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining({ createBranchBase: '' })))
    fireEvent.click(screen.getByRole('button', { name: 'Branch' }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'other' }))
    setSnapshot(current => ({
      ...current,
      currentBranch: 'main',
      attachedBranch: 'main',
    }))
    await waitFor(() => expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining({ createBranchBase: 'other' })))
  })
})

describe('GitOptions automatic base selection', () => {
  beforeEach(prepareGitMocks)

  it.each(['main', 'a1b2c3d'])('retains the no-op check for an attached branch %s', async (branch) => {
    const branches = [
      entry(branch),
    ]
    const onGitModeChange = vi.fn()
    renderGitOptions({
      gitMode: () => GitMode.SwitchBranch,
      gitInfo: makeGitInfo({ currentBranch: branch, attachedBranch: branch }),
      preloadedBranches: () => branches,
      onGitModeChange,
    })
    fireEvent.click(screen.getByRole('button', { name: 'Branch' }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: `${branch} (current)` }))
    await waitFor(() => expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining({
      checkoutBranchError: 'Working directory is already on this branch.',
    })))
  })

  it('clears an automatic base when a refreshed list proves detachment', async () => {
    const branches = [
      entry('main'),
    ]
    vi.mocked(workerGit.listGitBranches)
      .mockResolvedValueOnce({
        $typeName: 'leapmux.v1.ListGitBranchesResponse',
        branches,
        currentBranch: 'main',
        attachedBranch: 'main',
      })
      .mockResolvedValueOnce({
        $typeName: 'leapmux.v1.ListGitBranchesResponse',
        branches,
        currentBranch: 'a1b2c3d',
        attachedBranch: '',
      })
    const [refresh, setRefresh] = createSignal(0)
    const onGitModeChange = vi.fn()
    render(() => <GitOptions workerId="w1" selectedPath="/repo" gitInfo={makeGitInfo()} gitMode={() => GitMode.CreateBranch} onGitModeChange={onGitModeChange} refreshKey={refresh()} />)
    await waitFor(() => expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining({ createBranchBase: 'main' })))
    setRefresh(1)
    await waitFor(() => expect(workerGit.listGitBranches).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(onGitModeChange).toHaveBeenLastCalledWith(expect.objectContaining({ createBranchBase: '' })))
  })
})
