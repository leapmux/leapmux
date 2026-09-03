/// <reference types="vitest/globals" />
import type { GitBranchEntry } from '~/generated/proto/leapmux/v1/git_pb'
import type { GitInfoFields, GitPathInfo } from '~/hooks/useGitPathInfo'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
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
  // The set replaces an O(N) `some(... endsWith('/<input>'))` walk that
  // ran on every keystroke in the create-branch input. These tests pin
  // the suffix semantics so a future refactor can't quietly drop or
  // broaden a match. The strip mirrors `gitutil.StripRemotePrefix` /
  // frontend `stripRemotePrefix`: exactly ONE leading segment.

  it('returns an empty set for an empty input', () => {
    expect(existingNames([])).toEqual(new Set())
  })

  it('local branches contribute only their exact name', () => {
    // Locals with slashes ("feature/foo") must NOT yield "foo" — the
    // suffix strip is gated on isRemote and we preserve that
    // asymmetry.
    const set = existingNames([entry('main'), entry('feature/foo')])
    expect([...set].toSorted()).toEqual(['feature/foo', 'main'])
  })

  it('remote branches contribute the full ref + the single first-segment strip', () => {
    // "origin/feature/foo" → adds "origin/feature/foo" and
    // "feature/foo". It does NOT add "foo": the worker's
    // StripRemotePrefix strips a single segment per call, so `foo`
    // typed in CreateBranch mode cannot actually collide with
    // `origin/feature/foo` through the worker's collapse path.
    // Adding `foo` would over-block legitimate names on repos with
    // deep remote namespaces.
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
    // A local "foo" pre-existed; a remote "origin/foo" must add itself
    // and the bare "foo" — even though "foo" was already in the set.
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
    // Branch refs shouldn't contain `//`, but if a malformed value
    // sneaks in the strip must still be a single segment.
    const set = existingNames([entry('origin//x', true)])
    expect(set.has('origin//x')).toBe(true)
    expect(set.has('/x')).toBe(true)
    expect(set.has('x')).toBe(false)
  })

  it('a trailing slash adds the empty suffix (best-effort, harmless in practice)', () => {
    // `validateBranchName` rejects empty user input upstream, so the
    // empty entry here can never produce a false-positive collision —
    // but pin the behavior so a future loop guard that special-cases
    // empty strings doesn't accidentally drop legitimate suffixes.
    const set = existingNames([entry('origin/', true)])
    expect(set.has('origin/')).toBe(true)
    expect(set.has('')).toBe(true)
  })

  it('is idempotent on duplicate inputs', () => {
    // The branches() signal can carry the same entry twice during a
    // transient refresh; the set's size must stay bounded.
    const set = existingNames([
      entry('main'),
      entry('main'),
      entry('origin/main', true),
      entry('origin/main', true),
    ])
    expect([...set].toSorted()).toEqual(['main', 'origin/main'])
  })

  it('does not match across branches (single-segment strip is per-ref)', () => {
    // Two unrelated remote refs sharing a suffix: each contributes its
    // own first-segment strip independently; cross-ref deep suffixes
    // are NOT inferred.
    const set = existingNames([
      entry('origin/a/foo', true),
      entry('upstream/b/foo', true),
    ])
    // "origin/a/foo" → "a/foo"; "upstream/b/foo" → "b/foo".
    expect(set.has('a/foo')).toBe(true)
    expect(set.has('b/foo')).toBe(true)
    // The "any prefix of any branch" expansion is gone — bare `foo`
    // is no longer flagged just because some deep remote ref ends
    // with it.
    expect(set.has('foo')).toBe(false)
    expect(set.has('origin/b/foo')).toBe(false)
  })
})

describe('indexBranches', () => {
  // indexBranches replaces three independent walks (the local/remote
  // partition inside BranchSelect, the existing-name set built in
  // GitOptions, and the local-name set used for the
  // "remote shadows local" warning) with one pass. These tests pin
  // the combined output so the optimization can't quietly drop any
  // of the three views.

  it('returns empty sets and empty arrays for empty input', () => {
    const idx = indexBranches([])
    expect(idx.local).toEqual([])
    expect(idx.remote).toEqual([])
    expect(idx.localNames).toEqual(new Set())
    expect(idx.existingNames).toEqual(new Set())
  })

  it('preserves input order within each partition', () => {
    // The select renders branches in input order under <optgroup>; a
    // future Set/Map-based partition could silently reorder. Pin both
    // halves so the UI stays stable.
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
    // The composite assertion: every output is consistent with the
    // others (no fields out of sync). Specifically, every name in
    // localNames must also be in existingNames; every entry in
    // local must have isRemote=false; every entry in remote must
    // have isRemote=true.
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

// Component-level tests that pin GitOptions' active-mode ownership
// contract: the `gitMode` prop seeds the radio at mount only, and
// every subsequent mode change flows out via `onGitModeChange`. Real
// dialog callers don't mutate the parent's gitMode out-of-band, so
// these synthetic cases are what actually exercise the contract — the
// ChangeBranchDialog integration tests cover the radio-click path but
// can't observe the seed-only nature of the prop read.

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
    // The `gitMode` prop is read once with `untrack` to seed the
    // internal `activeMode` signal; later updates to the prop accessor
    // do NOT flow into the radio. A reactive read here would echo
    // `setMode(GitMode.CreateBranch)` into the checked state and break
    // the uni-directional emit contract.
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
    // gated on `enabledModes().has` -- while the emit effect still reports that
    // mode, so the dialog submits an intent it never showed. The remembered
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
    // Switching repos invalidates every selection the mode depends on --
    // checkout branch, base branch, worktree path, and the fetched lists
    // behind them -- so the mode goes back to the default with them rather
    // than pointing at a repo that no longer supplies its inputs.
    //
    // Untested until now, which is how e2e 073 kept asserting the OPPOSITE
    // ("git mode is preserved when switching between git repos") and failing
    // on every run for as long as this reset has existed.
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

  // Regression guard for the `|| randomSlug()` fallback that used to live
  // in the derived branchName() accessor: clearing the seeded random name
  // snapped the displayed value back to the stored slug, so the field
  // could never be empty and the "must not be empty" validation never
  // fired. The single-signal model now binds the literal value to the
  // input (matching the NewWorkspace title field), so an empty input
  // stays empty and surfaces the validation error instead of resetting.
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

    // Clear the field. The fix removes the `|| randomSlug()` fallback, so
    // the value must stay empty rather than snapping back to the slug.
    fireEvent.input(input, { target: { value: '' } })
    await Promise.resolve()
    await Promise.resolve()
    expect(input.value).toBe('')

    // The existing validateBranchName('') error is now reachable and must
    // render the message below the field (same UX as the NewWorkspace
    // title field).
    expect(screen.getByText('Branch name must not be empty')).toBeInTheDocument()
  })

  // The single-signal refactor changed Randomize from "swap the stored
  // slug AND clear the typed value" to "replace the field's value". The
  // observable difference is the post-clear case: after the user empties
  // the field, Randomize must repopulate it with a fresh slug (and clear
  // the now-stale empty-name error) rather than leaving it blank because
  // the empty typed value kept winning the old `||` fallback.
  it('repopulates the field with a fresh random name on Randomize after clearing', async () => {
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
    fireEvent.input(input, { target: { value: '' } })
    await Promise.resolve()
    await Promise.resolve()
    expect(input.value).toBe('')

    // Randomize must write a fresh slug back into the single signal,
    // displacing the empty value and clearing the validation error. The
    // button's `title` surfaces as an aria-label via Tooltip.
    fireEvent.click(screen.getByLabelText('Generate random name'))
    await Promise.resolve()
    await Promise.resolve()
    expect(input.value).not.toBe('')
    expect(screen.queryByText('Branch name must not be empty')).not.toBeInTheDocument()
  })
})
