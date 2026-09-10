/// <reference types="vitest/globals" />
import type { GitBranchEntry } from '~/generated/proto/leapmux/v1/git_pb'
import type { GitInfoFields, GitPathInfo } from '~/hooks/useGitPathInfo'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { generateSlug } from 'random-word-slugs'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { GitMode } from '~/hooks/useGitModeState'
import { dirtyWarningCopy, GitOptions, indexBranches } from './GitOptions'

function existingNames(branches: readonly GitBranchEntry[]): Set<string> {
  return indexBranches(branches).existingNames
}

function entry(name: string, isRemote = false): GitBranchEntry {
  return { $typeName: 'leapmux.v1.GitBranchEntry', name, isRemote } as GitBranchEntry
}

describe('dirtyWarningCopy', () => {
  it('explains the discard-or-fail risk for SwitchBranch', () => {
    const copy = dirtyWarningCopy(GitMode.SwitchBranch)
    expect(copy).toMatch(/uncommitted changes/i)
    // The switch warning is the only one that mentions failure/discard
    // — the other two transfer the changes one way or another.
    expect(copy).toMatch(/fail|discard/i)
  })

  it('says the new branch will inherit uncommitted changes for CreateBranch', () => {
    const copy = dirtyWarningCopy(GitMode.CreateBranch)
    expect(copy).toMatch(/uncommitted changes/i)
    expect(copy).toMatch(/include/i)
  })

  it('says the new worktree will NOT receive the changes for CreateWorktree', () => {
    const copy = dirtyWarningCopy(GitMode.CreateWorktree)
    expect(copy).toMatch(/uncommitted changes/i)
    // CreateWorktree is the inverse of CreateBranch: the new tree
    // starts clean, so the copy must say so explicitly.
    expect(copy).toMatch(/not be transferred/i)
  })

  it('returns null for modes without a dirty-tree warning', () => {
    expect(dirtyWarningCopy(GitMode.Current)).toBeNull()
    expect(dirtyWarningCopy(GitMode.UseWorktree)).toBeNull()
  })

  it('returns distinct copy per warning mode (no accidental shared strings)', () => {
    const a = dirtyWarningCopy(GitMode.SwitchBranch)
    const b = dirtyWarningCopy(GitMode.CreateBranch)
    const c = dirtyWarningCopy(GitMode.CreateWorktree)
    expect(a).not.toEqual(b)
    expect(b).not.toEqual(c)
    expect(a).not.toEqual(c)
  })
})

describe('indexBranches.existingNames', () => {
  // The set avoids a branch scan on every keystroke.
  // Preserve the suffix rules from gitutil.StripRemotePrefix and stripRemotePrefix: remove exactly one leading remote segment.

  it('returns an empty set for an empty input', () => {
    expect(existingNames([])).toEqual(new Set())
  })

  it('local branches contribute only their exact name', () => {
    // A local branch such as feature/foo must not add foo. Only remote branches lose their first segment.
    const set = existingNames([entry('main'), entry('feature/foo')])
    expect([...set].toSorted()).toEqual(['feature/foo', 'main'])
  })

  it('remote branches contribute the full ref + the single first-segment strip', () => {
    // origin/feature/foo adds itself and feature/foo. It must not add foo.
    // The worker removes only one segment, so foo cannot conflict through that conversion.
    // Adding foo would incorrectly reject valid branch names in deeper remote namespaces.
    const set = existingNames([entry('origin/feature/foo', true)])
    expect([...set].toSorted()).toEqual(['feature/foo', 'origin/feature/foo'])
    expect(set.has('foo')).toBe(false)
  })

  it('shallow remote refs add only the post-prefix name', () => {
    // "origin/foo" → adds "origin/foo" and "foo" (single segment strip).
    const set = existingNames([entry('origin/foo', true)])
    expect([...set].toSorted()).toEqual(['foo', 'origin/foo'])
  })

  it('merges local and remote contributions into one collision set', () => {
    // A remote origin/foo must add itself and foo even when a local foo already exists.
    const set = existingNames([entry('foo'), entry('origin/foo', true)])
    expect([...set].toSorted()).toEqual(['foo', 'origin/foo'])
  })

  it('local branch with a slash does NOT cause suffix-based collisions', () => {
    // Regression guard: only remote branches participate in the suffix
    // strip. A local "feature/foo" must not flag "foo".
    const set = existingNames([entry('feature/foo')])
    expect(set.has('feature/foo')).toBe(true)
    expect(set.has('foo')).toBe(false)
  })

  it('handles consecutive slashes in a malformed remote ref by stripping only the first segment', () => {
    // A malformed branch reference can contain //. Remove only one segment even for this invalid input.
    const set = existingNames([entry('origin//x', true)])
    expect(set.has('origin//x')).toBe(true)
    expect(set.has('/x')).toBe(true)
    expect(set.has('x')).toBe(false)
  })

  it('a trailing slash adds the empty suffix (best-effort, harmless in practice)', () => {
    // validateBranchName rejects empty input before this lookup, so an empty entry cannot cause a false conflict.
    // Keep this case to prevent an empty-string special case from removing valid suffixes.
    const set = existingNames([entry('origin/', true)])
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

  it('localNames is the set of local branch names only', () => {
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

vi.mock('random-word-slugs', () => ({ generateSlug: vi.fn(() => 'seeded-branch-name') }))

vi.mock('~/api/workerRpc', () => ({
  listGitBranches: vi.fn(),
  listGitWorktrees: vi.fn(),
}))

function makeGitInfo(overrides: Partial<GitInfoFields> = {}): GitPathInfo {
  const info: GitInfoFields = {
    isGitRepo: true,
    isRepoRoot: true,
    isWorktreeRoot: false,
    isDirty: false,
    repoRoot: '/repo',
    repoDirName: 'repo',
    currentBranch: 'main',
    errorHint: '',
    ...overrides,
  }
  return {
    loading: () => false,
    info: () => info,
    showGitOptions: () => true,
  }
}

describe('gitOptions activeMode ownership', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(workerRpc.listGitBranches).mockResolvedValue({
      $typeName: 'leapmux.v1.ListGitBranchesResponse',
      branches: [],
      currentBranch: 'main',
    })
    vi.mocked(workerRpc.listGitWorktrees).mockResolvedValue({
      $typeName: 'leapmux.v1.ListGitWorktreesResponse',
      worktrees: [],
    })
  })

  it('seeds the radio from `props.gitMode()` at mount and ignores subsequent external mutations', async () => {
    // Read gitMode once with untrack to initialize activeMode. Later prop updates must not replace the internal mode.
    const [mode, setMode] = createSignal<GitMode>(GitMode.SwitchBranch)
    const onGitModeChange = vi.fn()

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
      />
    ))

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
    // The click path goes `setActiveMode → currentIntent recompute →
    // effect → onGitModeChange`. Asserts both the visual flip AND the
    // emit so the parent's view of the mode (which comes from the
    // intent, not from re-reading the prop) stays in sync.
    const [mode] = createSignal<GitMode>(GitMode.SwitchBranch)
    const onGitModeChange = vi.fn()

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
      />
    ))

    await waitFor(() => expect(screen.getByLabelText('Switch to branch')).toBeChecked())
    onGitModeChange.mockClear()

    fireEvent.click(screen.getByLabelText('Create new branch'))

    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())
    expect(screen.getByLabelText('Switch to branch')).not.toBeChecked()

    // The emit fires through the createMemo + createEffect path, so it
    // may take a microtask hop. Wait for the assertion rather than
    // asserting synchronously.
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

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
        modes={[GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree]}
      />
    ))

    await waitFor(() => expect(onGitModeChange).toHaveBeenCalled())
    const firstIntent = onGitModeChange.mock.calls[0][0] as { mode: GitMode }
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

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
        modes={[]}
      />
    ))

    // DEFAULT_GIT_MODES includes Current as the first/default mode, so
    // the seed intent emitted on mount must NOT be undefined.
    await waitFor(() => expect(onGitModeChange).toHaveBeenCalled())
    const firstIntent = onGitModeChange.mock.calls[0][0] as { mode: GitMode | undefined }
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

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
        modes={[GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree]}
      />
    ))

    // The first enabled mode, because Current is not enabled either.
    await waitFor(() => expect(screen.getByLabelText('Switch to branch')).toBeChecked())
    await waitFor(() => expect(onGitModeChange).toHaveBeenCalled())
    const firstIntent = onGitModeChange.mock.calls[0][0] as { mode: GitMode }
    expect(firstIntent.mode).toBe(GitMode.SwitchBranch)
  })

  it('clamps to Current when the enabled set includes it', async () => {
    const [mode] = createSignal<GitMode>(GitMode.UseWorktree)
    const onGitModeChange = vi.fn()

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
        modes={[GitMode.Current, GitMode.SwitchBranch]}
      />
    ))

    await waitFor(() => expect(screen.getByLabelText('Use current state')).toBeChecked())
    expect(screen.getByLabelText('Switch to branch')).not.toBeChecked()
  })

  it('passes a seed that IS in the enabled set through unchanged', async () => {
    // The guard rail on the clamp above: a legitimate seed must not be
    // rewritten to the default.
    const [mode] = createSignal<GitMode>(GitMode.CreateWorktree)
    const onGitModeChange = vi.fn()

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
        modes={[GitMode.SwitchBranch, GitMode.CreateBranch, GitMode.CreateWorktree]}
      />
    ))

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

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={vi.fn()}
        modes={modes()}
      />
    ))

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

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={vi.fn()}
        modes={modes()}
      />
    ))

    await waitFor(() => expect(screen.getByLabelText('Use current state')).toBeChecked())

    setModes([GitMode.SwitchBranch, GitMode.CreateBranch])

    await waitFor(() => expect(screen.getByLabelText('Switch to branch')).toBeChecked())
  })

  it('honours a seed inside DEFAULT_GIT_MODES when modes is the empty array', async () => {
    // The second guard rail: an empty `modes` falls back to the default set,
    // so the clamp must judge the seed against THAT set and not refuse it.
    const [mode] = createSignal<GitMode>(GitMode.UseWorktree)
    const onGitModeChange = vi.fn()

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
        modes={[]}
      />
    ))

    await waitFor(() => expect(screen.getByLabelText('Use existing worktree')).toBeChecked())
  })

  it('resets the mode to the default when the selected path changes', async () => {
    // A repository change invalidates the checkout branch, base branch, and worktree path. It also replaces their source lists.
    // Reset the mode also, so it cannot retain selections from the previous repository.
    const [mode] = createSignal<GitMode>(GitMode.Current)
    const [path, setPath] = createSignal('/repo')
    const onGitModeChange = vi.fn()

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath={path()}
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
      />
    ))

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

  it('keeps the mode when the selected path is set to the same value', async () => {
    // The reset is keyed on the path CHANGING. A re-render that hands back
    // an identical path (a parent re-computing the same string) must not
    // throw away a mode the user just picked.
    const [mode] = createSignal<GitMode>(GitMode.Current)
    const [path, setPath] = createSignal('/repo')
    const onGitModeChange = vi.fn()

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath={path()}
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
      />
    ))

    fireEvent.click(screen.getByLabelText('Create new branch'))
    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())

    setPath('/repo')
    await Promise.resolve()
    await Promise.resolve()

    expect(screen.getByLabelText('Create new branch')).toBeChecked()
  })
})

describe('gitOptions branch name field', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(workerRpc.listGitBranches).mockResolvedValue({
      $typeName: 'leapmux.v1.ListGitBranchesResponse',
      branches: [],
      currentBranch: 'main',
    })
    vi.mocked(workerRpc.listGitWorktrees).mockResolvedValue({
      $typeName: 'leapmux.v1.ListGitWorktreesResponse',
      worktrees: [],
    })
  })

  // An empty branch field must remain empty and show its validation error.
  // A randomSlug fallback previously restored the generated value and made the empty-input path unreachable.
  // One signal now supplies the literal field value, as the NewWorkspace title field does.
  it('keeps the field empty and shows an error when the default name is cleared', async () => {
    const [mode] = createSignal<GitMode>(GitMode.CreateBranch)
    const onGitModeChange = vi.fn()

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
        modes={[GitMode.CreateBranch]}
      />
    ))

    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())

    const input = screen.getByPlaceholderText('feature-branch') as HTMLInputElement
    // Seeded with a non-empty random slug.
    expect(input.value).not.toBe('')

    // Clear the field. Its value must remain empty instead of restoring the generated value.
    fireEvent.input(input, { target: { value: '' } })
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

    render(() => (
      <GitOptions
        workerId="w1"
        selectedPath="/repo"
        gitInfo={makeGitInfo()}
        gitMode={mode}
        onGitModeChange={onGitModeChange}
        modes={[GitMode.CreateBranch]}
      />
    ))

    await waitFor(() => expect(screen.getByLabelText('Create new branch')).toBeChecked())

    const input = screen.getByPlaceholderText('feature-branch') as HTMLInputElement
    expect(input.value).toBe('first-branch')
    fireEvent.click(screen.getByLabelText('Generate random name'))
    expect(input.value).toBe('second-branch')
    fireEvent.input(input, { target: { value: '' } })
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
