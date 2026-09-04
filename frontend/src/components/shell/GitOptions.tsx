import type { Accessor, Component, JSX } from 'solid-js'
import type { GitBranchEntry } from '~/generated/proto/leapmux/v1/git_pb'
import type { GitModeIntent } from '~/hooks/useGitModeState'
import type { GitPathInfo } from '~/hooks/useGitPathInfo'
import { generateSlug } from 'random-word-slugs'
import { batch, createEffect, createMemo, createSignal, on, Show, untrack } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { labelRow, pathPreview, radioGroup, radioRow, radioSubContent } from '~/components/common/Dialog.css'
import { LabeledField } from '~/components/common/LabeledField'
import { RefreshButton } from '~/components/common/RefreshButton'
import { Tooltip } from '~/components/common/Tooltip'
import { WorktreeSelect } from '~/components/shell/WorktreeSelect'
import { BranchSelect } from '~/components/workspace/BranchSelect'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'
import { GIT_MODE_LABELS, GitMode } from '~/hooks/useGitModeState'
import { createLogger } from '~/lib/logger'
import { detectFlavor, join, parentDirectory, tildify } from '~/lib/paths'
import { stripRemotePrefix, validateBranchName } from '~/lib/validate'
import { warningText } from '~/styles/shared.css'

const log = createLogger('GitOptions')

interface GitOptionsProps {
  workerId: string
  selectedPath: string
  homeDir?: string
  /**
   * Resolved repo identity (loading, isGitRepo, repoRoot, etc.) sourced
   * from `useGitPathInfo`. GitOptions is a pure renderer — it does NOT
   * issue the getGitInfo probe itself; the caller owns the probe and
   * decides when to mount this component (typically only when
   * `gitInfo.showGitOptions()` is true). Fields are read via
   * `gitInfo.info().X`.
   */
  gitInfo: GitPathInfo
  /**
   * Seed mode for the radio selection. Read ONCE on mount (untracked)
   * and then ignored — GitOptions owns the active mode internally and
   * emits every change via `onGitModeChange`. Pass the parent's
   * `useGitModeState.gitMode` accessor here: its current value at
   * mount-time seeds the radio; subsequent updates flow OUT (radio
   * click → `onGitModeChange`), not IN.
   */
  gitMode: Accessor<GitMode>
  /**
   * Emits a tagged intent every time the user's mode or any mode-relevant
   * field changes. Each variant only carries the fields its mode
   * consumes, so consumers can switch on `intent.mode` without optional-
   * field bookkeeping.
   */
  onGitModeChange: (intent: GitModeIntent) => void
  refreshKey?: number
  /**
   * Which git modes to render as selectable radios. Defaults to all five.
   * Pre-seed the parent's intent via `useGitModeState(initial)` to
   * select a non-default starting mode.
   */
  modes?: readonly GitMode[]
  /**
   * Pre-fetched branches list. When set, GitOptions reads its branches
   * from this accessor instead of issuing its own ListGitBranches RPC.
   * Used by dialogs that already bundle the probe + branches into a
   * single round trip (e.g. ChangeBranchDialog → InspectBranchChange):
   * passing this prop drops the redundant ListGitBranches call.
   *
   * The accessor returns `null` while the parent's bundle RPC is still
   * in flight; GitOptions treats that the same as a loading state.
   */
  preloadedBranches?: Accessor<GitBranchEntry[] | null>
  /**
   * Loading flag paired with {@link preloadedBranches}. While true the
   * dialog's BranchSelect renders its "Loading branches..." placeholder
   * even when `preloadedBranches()` is empty/null.
   */
  preloadedBranchesLoading?: Accessor<boolean>
  /**
   * Refresh hook paired with {@link preloadedBranches}. Invoked when the
   * user clicks the Refresh button while a branches-consuming mode is
   * active. The parent is expected to re-issue its bundle RPC.
   */
  onRefreshBranches?: () => void
}

const DEFAULT_GIT_MODES: readonly GitMode[] = [
  GitMode.Current,
  GitMode.SwitchBranch,
  GitMode.CreateBranch,
  GitMode.CreateWorktree,
  GitMode.UseWorktree,
]

export interface BranchIndex {
  /** Local branches, in original input order. */
  local: GitBranchEntry[]
  /** Remote branches, in original input order. */
  remote: GitBranchEntry[]
  /**
   * Every name that should count as a collision when the user types a
   * new branch name. Local branches contribute their exact name; remote
   * branches additionally contribute the FIRST `/`-separated suffix
   * (mirroring `gitutil.StripRemotePrefix` and the frontend
   * `stripRemotePrefix` helper, which both strip exactly one leading
   * segment). A remote `origin/feature/foo` flags `feature/foo` but
   * NOT `foo` — `foo` cannot collide via the worker's single-segment
   * strip, and the earlier "expand every `/`" pass over-blocked
   * legitimate names users wanted to type on repos with deep remote
   * namespaces.
   */
  existingNames: Set<string>
  /** Local branch names only — used to detect "this remote shadows an existing local". */
  localNames: Set<string>
}

/**
 * Walk `branches` once and produce the local/remote partition plus the
 * collision-name sets the dialog's validation needs. Exported so unit
 * tests can pin the remote suffix-expansion semantics without mounting
 * GitOptions.
 */
export function indexBranches(branches: readonly GitBranchEntry[]): BranchIndex {
  const local: GitBranchEntry[] = []
  const remote: GitBranchEntry[] = []
  const existingNames = new Set<string>()
  const localNames = new Set<string>()
  for (const b of branches) {
    existingNames.add(b.name)
    if (b.isRemote) {
      remote.push(b)
      const idx = b.name.indexOf('/')
      if (idx !== -1) {
        existingNames.add(b.name.slice(idx + 1))
      }
    }
    else {
      local.push(b)
      localNames.add(b.name)
    }
  }
  return { local, remote, existingNames, localNames }
}

/**
 * A `Record`, not a `switch` with a `default`: a sixth mode added to `GitMode`
 * is then a compile error here rather than a mode that silently falls through
 * to "no warning". `null` states that a mode deliberately shows none.
 */
const DIRTY_WARNINGS: Record<GitMode, string | null> = {
  [GitMode.Current]: null,
  [GitMode.SwitchBranch]: 'The working copy has uncommitted changes. Switching branches may fail or discard changes.',
  [GitMode.CreateBranch]: 'The working copy has uncommitted changes. Creating a new branch will include them.',
  [GitMode.CreateWorktree]: 'The selected working copy has uncommitted changes that will not be transferred to the new worktree.',
  [GitMode.UseWorktree]: null,
}

/**
 * Per-mode dirty-worktree warning copy. Exported so tests can pin the exact
 * strings shown for each mode without rendering the full GitOptions component
 * (which depends on auth context, RPC mocks, etc.). Returns null for a mode
 * that surfaces no dirty-tree warning.
 */
export function dirtyWarningCopy(mode: GitMode): string | null {
  return DIRTY_WARNINGS[mode]
}

/** The branch-name error node's id, so its input can point at it. */
const BRANCH_NAME_ERROR_ID = 'git-options-branch-name-error'

export const GitOptions: Component<GitOptionsProps> = (props) => {
  // `?? DEFAULT_GIT_MODES` only triggers on null/undefined, not on an
  // empty array — but an empty array is the more dangerous shape: it
  // would yield `enabledModeList()[0] === undefined` below and the
  // active-mode signal would be left at `undefined`, breaking every
  // downstream switch on a value the type system claims is impossible.
  // Treat empty as missing and fall back to the default set.
  const enabledModeList = () => {
    const m = props.modes
    return m && m.length > 0 ? m : DEFAULT_GIT_MODES
  }
  // Memoized Set so the per-mode `enabledModes().has(...)` checks in the
  // radio render loop are O(1) and recompute only when `props.modes`
  // identity actually changes.
  const enabledModes = createMemo(() => new Set(enabledModeList()))
  // The mode this dialog falls back to on a (worker, path) reset. Picks
  // Current when enabled, else the first enabled mode (e.g.
  // ChangeBranchDialog excludes Current and defaults to SwitchBranch).
  const defaultMode = (): GitMode => enabledModes().has(GitMode.Current) ? GitMode.Current : enabledModeList()[0]

  // Active mode is owned here, not in the parent. The seed comes from
  // `props.gitMode()`, read once with `untrack` so subsequent parent
  // updates to that accessor are ignored — mode changes flow OUT via
  // `onGitModeChange`, not IN via the prop. The parent's `gitMode`
  // (e.g. `useGitModeState.gitMode`) reflects the emitted intent's
  // `.mode` for its own conditional renders, but that value never
  // re-enters GitOptions.
  //
  // The active mode is DERIVED, not stored. A mode outside the enabled set
  // leaves every radio unchecked -- each row is `enabledModes().has` -- while
  // the emit effect still reports that mode's intent, so the dialog submits a
  // mode it never showed. The remembered per-repository mode makes that
  // reachable: a mode stored by one dialog can be absent from the next one's
  // set. Deriving makes the state unrepresentable rather than repairing it at
  // the two moments it can arise (the seed, and a set that narrows later).
  const clampMode = (mode: GitMode): GitMode =>
    enabledModes().has(mode) ? mode : defaultMode()
  const [rawMode, setRawMode] = createSignal<GitMode>(untrack(() => props.gitMode()))
  const activeMode = createMemo(() => clampMode(rawMode()))

  // Branch-name UX. CreateBranch and CreateWorktree are mutually
  // exclusive radios that both render the same "Branch name" input +
  // Randomize button. One shared signal holds the name (seeded with a
  // random slug), staying alive across a toggle; Randomize swaps in a
  // fresh slug, which is visible in whichever mode is currently active.
  // The literal value is bound directly to the input, so clearing it
  // leaves the field empty and surfaces the existing "must not be empty"
  // validation — matching the NewWorkspace title field, rather than
  // silently snapping back to a stored slug.
  const [branchName, setBranchName] = createSignal(generateSlug(3, { format: 'kebab' }))

  // Branch list for switch-branch and base branch selector. Either driven
  // by the internal `ListGitBranches` fetcher OR seeded from the parent's
  // bundle RPC via `preloadedBranches` — `branches()` below is the memo
  // that picks whichever source the dialog is using.
  const [internalBranches, setInternalBranches] = createSignal<GitBranchEntry[]>([])
  const branches = createMemo<GitBranchEntry[]>(() => props.preloadedBranches?.() ?? internalBranches())
  const [selectedCheckoutBranch, setSelectedCheckoutBranch] = createSignal('')
  // Base branch picker. Shared between CreateBranch and CreateWorktree
  // for the same reason as branchName above.
  const [selectedBaseBranch, setSelectedBaseBranch] = createSignal('')

  // Worktree list for use-worktree mode. The main working tree is
  // filtered out below, so every stored entry is a linked worktree.
  const [worktrees, setWorktrees] = createSignal<{ path: string, branch: string }[]>([])
  const [selectedWorktreePath, setSelectedWorktreePath] = createSignal('')

  // Single pass over branches() that yields the local/remote partition
  // AND the existing-name / local-name sets every downstream check
  // needs. The `local` / `remote` arrays feed every BranchSelect this
  // dialog renders (one per Switch/CreateBase) so the picker doesn't
  // re-partition on its own. See {@link indexBranches} for the exact
  // remote suffix-expansion semantics.
  const branchIndex = createMemo(() => indexBranches(branches()))

  const branchExists = (name: string) => branchIndex().existingNames.has(name)

  const branchError = createMemo(() => {
    const formatErr = validateBranchName(branchName())
    if (formatErr)
      return formatErr
    if (branchExists(branchName()))
      return 'A branch with this name already exists'
    return null
  })

  const worktreePath = () => {
    const i = props.gitInfo.info()
    if (!i.repoRoot || !branchName())
      return ''
    const flavor = detectFlavor(i.repoRoot)
    return join([parentDirectory(i.repoRoot, flavor), `${i.repoDirName}-worktrees`, branchName()], flavor)
  }

  const localBranchNames = createMemo(() => branchIndex().localNames)

  // Hint when the SwitchBranch destination would resolve to the branch
  // the working directory is already on. The dialog also blocks submit
  // via isChangeBranchSubmitDisabled / isSwitchBranchNoop — this memo
  // is the user-facing copy that explains why Apply is disabled.
  // Handles both a direct local match and a remote ref (e.g.
  // `origin/main` while on `main`) whose strip-to-local equals current.
  const checkoutBranchNoopNotice = createMemo(() => {
    // eslint-disable-next-line solid/reactivity -- `selected` is read in this same memo body, not in a deferred callback
    const selected = selectedCheckoutBranch()
    if (!selected)
      return null
    const cur = props.gitInfo.info().currentBranch
    if (!cur)
      return null
    if (selected === cur)
      return 'Working directory is already on this branch.'
    const entry = branches().find(b => b.name === selected)
    if (entry?.isRemote && stripRemotePrefix(selected) === cur)
      return `Working directory is already on local branch "${cur}".`
    return null
  })

  // Warn when a remote branch is selected but a local branch with the
  // same name already exists AND that local isn't the current branch.
  // The "current" overlap is surfaced by checkoutBranchNoopNotice with
  // submit disabled — this warning is for the still-actionable case.
  const checkoutBranchWarning = createMemo(() => {
    // eslint-disable-next-line solid/reactivity -- `selected` is read in this same memo body, not in a deferred callback
    const selected = selectedCheckoutBranch()
    if (!selected)
      return null
    const entry = branches().find(b => b.name === selected)
    if (!entry?.isRemote)
      return null
    const localName = stripRemotePrefix(selected)
    if (!localBranchNames().has(localName))
      return null
    if (localName === props.gitInfo.info().currentBranch)
      return null
    return `Local branch "${localName}" already exists and will be checked out instead.`
  })

  // Lazy fetcher for switch-branch / create-branch / create-worktree
  // modes. The guarded helper batches the loading flag with the list
  // update on success, so <BranchSelect> never renders a list and a
  // loading state that disagree. The `<select>` this replaced needed that
  // atomicity to keep the browser from resetting `selectedIndex`; a menu
  // derives its checked item from `value` on every render, so what the
  // batching buys now is that the menu's own loading gate and its options
  // flip together. Skipped entirely when the parent supplies
  // `preloadedBranches`.
  const branchFetcher = createGuardedFetch<{ wid: string, path: string }, Awaited<ReturnType<typeof workerRpc.listGitBranches>>>({
    fetch: ({ wid, path }) => workerRpc.listGitBranches(wid, { workerId: wid, path }),
    applySuccess: (resp) => {
      setInternalBranches(resp.branches)
      const cur = resp.currentBranch || props.gitInfo.info().currentBranch
      if (!selectedBaseBranch() && cur) {
        setSelectedBaseBranch(cur)
      }
    },
    onError: err => log.warn('Failed to list git branches', err),
  })
  const branchesLoading = () => props.preloadedBranchesLoading?.() ?? branchFetcher.loading()

  const fetchBranches = async () => {
    // When the parent owns the branches list, refresh delegates back to
    // it (e.g. ChangeBranchDialog re-issues its InspectBranchChange).
    if (props.preloadedBranches) {
      props.onRefreshBranches?.()
      return
    }
    if (!props.workerId || !props.selectedPath)
      return
    await branchFetcher.run({ wid: props.workerId, path: props.selectedPath })
  }

  // Seed selectedBaseBranch from the current branch once branches land,
  // regardless of which source supplied them. Mirrors the seeding logic
  // that previously lived inside `branchFetcher.applySuccess`, so the
  // preloaded-branches path also picks the right default.
  createEffect(() => {
    if (branches().length > 0 && !selectedBaseBranch()) {
      const cur = props.gitInfo.info().currentBranch
      if (cur)
        setSelectedBaseBranch(cur)
    }
  })

  const worktreeFetcher = createGuardedFetch<{ wid: string, path: string }, Awaited<ReturnType<typeof workerRpc.listGitWorktrees>>>({
    fetch: ({ wid, path }) => workerRpc.listGitWorktrees(wid, { workerId: wid, path }),
    applySuccess: (resp) => {
      // Only show actual worktrees (exclude the main working tree).
      setWorktrees(resp.worktrees.filter(wt => !wt.isMain).map(wt => ({ path: wt.path, branch: wt.branch })))
    },
    onError: err => log.warn('Failed to list git worktrees', err),
  })
  const worktreesLoading = worktreeFetcher.loading

  const fetchWorktrees = async () => {
    if (!props.workerId || !props.selectedPath)
      return
    await worktreeFetcher.run({ wid: props.workerId, path: props.selectedPath })
  }

  // Build the intent for a given mode using whatever typed values are
  // live right now. Shared by the per-mode auto-emit effect, the radio
  // click handler, and the (worker, path) reset — keeping all three in
  // lockstep means a new mode variant only needs editing here.
  const intentFor = (mode: GitMode): GitModeIntent => {
    switch (mode) {
      case GitMode.Current:
        return { mode }
      case GitMode.SwitchBranch:
        return {
          mode,
          checkoutBranch: selectedCheckoutBranch(),
          checkoutBranchError: checkoutBranchNoopNotice(),
        }
      case GitMode.CreateBranch:
        return {
          mode,
          createBranch: branchName(),
          createBranchError: branchError(),
          createBranchBase: selectedBaseBranch(),
        }
      case GitMode.CreateWorktree:
        return {
          mode,
          worktreeBranch: branchName(),
          worktreeBranchError: branchError(),
          worktreeBaseBranch: selectedBaseBranch(),
        }
      case GitMode.UseWorktree:
        return { mode, useWorktreePath: selectedWorktreePath() }
    }
  }

  // Reset secondary lists + selections when worker / path changes. The
  // activeMode signal is reset to the dialog's default mode here; the
  // currentIntent memo + emit effect below picks up the change and
  // notifies the parent in the same flush.
  //
  // `props.modes` is deliberately NOT a dependency: `activeMode` is derived
  // through `clampMode`, so a set that narrows re-derives on its own and needs
  // no reset -- which also stops a narrowing from discarding the branch list,
  // the worktrees and all three selections along with a still-valid pick.
  createEffect(on(() => [props.workerId, props.selectedPath] as const, () => {
    batch(() => {
      setInternalBranches([])
      setWorktrees([])
      setSelectedCheckoutBranch('')
      setSelectedBaseBranch('')
      setSelectedWorktreePath('')
      setRawMode(defaultMode())
    })
  }, { defer: true }))

  // Whether the active mode consumes the branches list vs. the
  // worktrees list. Both effects below dispatch through this so adding
  // a new mode that needs branches only touches the predicate.
  const modeNeedsBranches = (mode: GitMode) =>
    mode === GitMode.SwitchBranch || mode === GitMode.CreateBranch || mode === GitMode.CreateWorktree
  const modeNeedsWorktrees = (mode: GitMode) => mode === GitMode.UseWorktree

  const refetchForMode = (mode: GitMode) => {
    if (modeNeedsBranches(mode))
      fetchBranches()
    if (modeNeedsWorktrees(mode))
      fetchWorktrees()
  }

  // Fetch branches/worktrees when the path resolves to a known repo and
  // the active mode needs them. The combined effect re-runs whenever
  // either readiness or the mode changes; the `length === 0` guards make
  // mode toggles a no-op once a list is cached. When the parent supplies
  // `preloadedBranches`, the branches list is parent-owned — skip the
  // auto-fetch (the parent's bundle RPC is the single source).
  createEffect(on(() => [props.gitInfo.showGitOptions(), activeMode()] as const, ([ready, mode]) => {
    if (!ready)
      return
    if (modeNeedsBranches(mode) && !props.preloadedBranches && branches().length === 0)
      fetchBranches()
    if (modeNeedsWorktrees(mode) && worktrees().length === 0)
      fetchWorktrees()
  }))

  // Re-fetch branches/worktrees when the refresh button is clicked.
  createEffect(on(() => props.refreshKey, () => {
    refetchForMode(activeMode())
  }, { defer: true }))

  // Single uni-directional emit point: radio clicks and typed input
  // changes flow through `setActiveMode` / `setBranchName` / etc.
  // → `intentFor` recomputes → this effect fires → parent's `setIntent`
  // lands. Dedup lives at the parent's signal (its `equals: shallowEqual`
  // skips notification for identity-equal intents) so no-op recomputes
  // here cost only one object allocation, not a downstream cascade.
  createEffect(() => props.onGitModeChange(intentFor(activeMode())))

  const randomizeBranch = () => {
    setBranchName(generateSlug(3, { format: 'kebab' }))
  }

  const renderDirtyWarning = (mode: GitMode) => (
    <Show when={props.gitInfo.info().isDirty}>
      <div class={warningText}>{dirtyWarningCopy(mode)}</div>
    </Show>
  )

  const renderBranchSelect = (selectProps: {
    value: string
    onChange: (v: string) => void
    showCurrent?: boolean
  }) => (
    <BranchSelect
      value={selectProps.value}
      onChange={selectProps.onChange}
      local={branchIndex().local}
      remote={branchIndex().remote}
      loading={branchesLoading()}
      currentBranch={props.gitInfo.info().currentBranch}
      showCurrent={selectProps.showCurrent}
    />
  )

  // Shared "Branch Name + Randomize + error" + "Base Branch picker"
  // block. CreateBranch and CreateWorktree both render this identical
  // pair of fields under the same branchName()/branchError()/
  // selectedBaseBranch() signals — the only thing that varies is the
  // optional worktree-path preview the caller slots in below.
  const renderBranchNameAndBase = () => (
    <>
      <LabeledField
        label="Branch Name"
        actions={<RefreshButton onClick={randomizeBranch} title="Generate random name" />}
        error={branchError()}
        errorId={BRANCH_NAME_ERROR_ID}
      >
        <input
          type="text"
          aria-label="Branch Name"
          aria-invalid={branchError() ? 'true' : undefined}
          aria-describedby={branchError() ? BRANCH_NAME_ERROR_ID : undefined}
          value={branchName()}
          onInput={e => setBranchName(e.currentTarget.value)}
          placeholder="feature-branch"
        />
      </LabeledField>
      <LabeledField label="Base Branch">
        {renderBranchSelect({ value: selectedBaseBranch(), onChange: setSelectedBaseBranch, showCurrent: true })}
      </LabeledField>
    </>
  )

  // Per-mode row: enabledModes() controls the whole block, the active mode
  // controls the sub-content. Callers own the `<div class={radioSubContent}>`
  // wrapper inside `children` so the Current mode can omit it when there's
  // no branch to display (an empty wrapper would still take a `gap` slot
  // in `radioGroup`).
  //
  // The label comes from the shared table, NOT from a prop: pairing a mode with
  // another mode's label is then impossible, and the menu item that opens this
  // dialog on that radio reads the same table.
  const ModeRadio: Component<{ mode: GitMode, children?: JSX.Element }> = rp => (
    <Show when={enabledModes().has(rp.mode)}>
      <label class={radioRow}>
        <input
          type="radio"
          name="git-mode"
          checked={activeMode() === rp.mode}
          onChange={() => setRawMode(rp.mode)}
        />
        {GIT_MODE_LABELS[rp.mode]}
      </label>
      <Show when={activeMode() === rp.mode}>
        {rp.children}
      </Show>
    </Show>
  )

  return (
    <div class="vstack gap-2">
      <div class={labelRow}>Git options</div>
      <div class={radioGroup}>
        <ModeRadio mode={GitMode.Current}>
          <Show when={props.gitInfo.info().currentBranch}>
            <div class={radioSubContent}>
              <div class={pathPreview}>
                {'Currently on branch: '}
                {props.gitInfo.info().currentBranch}
              </div>
            </div>
          </Show>
        </ModeRadio>

        <ModeRadio mode={GitMode.SwitchBranch}>
          <div class={radioSubContent}>
            {renderDirtyWarning(GitMode.SwitchBranch)}
            {renderBranchSelect({
              value: selectedCheckoutBranch(),
              onChange: setSelectedCheckoutBranch,
              // Label the current branch with `(current)` so the user
              // can see which one is the no-op pick — paired with the
              // checkoutBranchNoopNotice + disabled Apply below.
              showCurrent: true,
            })}
            <Show when={checkoutBranchNoopNotice()}>
              <div class={warningText}>{checkoutBranchNoopNotice()}</div>
            </Show>
            <Show when={checkoutBranchWarning()}>
              <div class={warningText}>{checkoutBranchWarning()}</div>
            </Show>
          </div>
        </ModeRadio>

        <ModeRadio mode={GitMode.CreateBranch}>
          <div class={radioSubContent}>
            {renderDirtyWarning(GitMode.CreateBranch)}
            {renderBranchNameAndBase()}
          </div>
        </ModeRadio>

        <ModeRadio mode={GitMode.CreateWorktree}>
          <div class={radioSubContent}>
            {renderDirtyWarning(GitMode.CreateWorktree)}
            {renderBranchNameAndBase()}
            <Show when={worktreePath()}>
              <div class={pathPreview}>
                Worktree path:
                {' '}
                <Tooltip text={worktreePath()}><code>{tildify(worktreePath(), props.homeDir)}</code></Tooltip>
              </div>
            </Show>
          </div>
        </ModeRadio>

        <ModeRadio mode={GitMode.UseWorktree}>
          <div class={radioSubContent}>
            <WorktreeSelect
              value={selectedWorktreePath()}
              onChange={setSelectedWorktreePath}
              worktrees={worktrees()}
              loading={worktreesLoading()}
              homeDir={props.homeDir}
            />
          </div>
        </ModeRadio>
      </div>
    </div>
  )
}
