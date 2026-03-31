import type { Accessor, Component, Setter } from 'solid-js'
import type { GitBranchEntry } from '~/generated/leapmux/v1/git_pb'
import type { GitMode } from '~/hooks/createWorkerDialogState'
import { generateSlug } from 'random-word-slugs'
import { batch, createEffect, createMemo, createSignal, For, on, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { tildify } from '~/components/chat/messageUtils'
import { RefreshButton } from '~/components/common/RefreshButton'
import { Tooltip } from '~/components/common/Tooltip'
import { useOrg } from '~/context/OrgContext'
import { createLogger } from '~/lib/logger'
import { validateBranchName } from '~/lib/validate'
import { errorText, labelRow, pathPreview, radioGroup, radioRow, radioSubContent, warningText } from '~/styles/shared.css'

const log = createLogger('GitOptions')
const LAST_PATH_SEGMENT_RE = /\/[^/]+$/
const REMOTE_PREFIX_RE = /^[^/]+\//

interface GitOptionsProps {
  workerId: string
  selectedPath: string
  homeDir?: string
  onGitModeChange: (
    mode: GitMode,
    opts: {
      checkoutBranch?: string
      worktreeBranch?: string
      worktreeBranchError?: string | null
      useWorktreePath?: string
      worktreeBaseBranch?: string
      createBranch?: string
      createBranchError?: string | null
      createBranchBase?: string
    },
  ) => void
  refreshKey?: number
  onVisibilityChange?: (visible: boolean) => void
}

export const GitOptions: Component<GitOptionsProps> = (props) => {
  const org = useOrg()
  const [isGitRepo, setIsGitRepo] = createSignal(false)
  const [isRepoRoot, setIsRepoRoot] = createSignal(false)
  const [isWorktreeRoot, setIsWorktreeRoot] = createSignal(false)
  const [isDirty, setIsDirty] = createSignal(false)
  const [repoRoot, setRepoRoot] = createSignal('')
  const [repoDirName, setRepoDirName] = createSignal('')
  const [currentBranch, setCurrentBranch] = createSignal('')
  const [gitMode, setGitMode] = createSignal<GitMode>('current')
  const [branchName, setBranchName] = createSignal(generateSlug(3, { format: 'kebab' }))
  const [loading, setLoading] = createSignal(false)

  // Branch list for switch-branch and base branch selector
  const [branches, setBranches] = createSignal<GitBranchEntry[]>([])
  const [branchesLoading, setBranchesLoading] = createSignal(false)
  const [selectedCheckoutBranch, setSelectedCheckoutBranch] = createSignal('')
  const [selectedBaseBranch, setSelectedBaseBranch] = createSignal('')

  // Create-branch mode
  const [newBranchName, setNewBranchName] = createSignal(generateSlug(3, { format: 'kebab' }))
  const [selectedNewBranchBase, setSelectedNewBranchBase] = createSignal('')

  // Worktree list for use-worktree mode
  const [worktrees, setWorktrees] = createSignal<{ path: string, branch: string, isMain: boolean }[]>([])
  const [worktreesLoading, setWorktreesLoading] = createSignal(false)
  const [selectedWorktreePath, setSelectedWorktreePath] = createSignal('')

  const branchExists = (name: string) =>
    branches().some(b => b.name === name || (b.isRemote && b.name.endsWith(`/${name}`)))

  const branchError = createMemo(() => {
    const formatErr = validateBranchName(branchName())
    if (formatErr)
      return formatErr
    if (branchExists(branchName()))
      return 'A branch with this name already exists'
    return null
  })
  const newBranchError = createMemo(() => {
    const formatErr = validateBranchName(newBranchName())
    if (formatErr)
      return formatErr
    if (branchExists(newBranchName()))
      return 'A branch with this name already exists'
    return null
  })

  const showGitOptions = () => isGitRepo() && (isRepoRoot() || isWorktreeRoot())

  const worktreePath = () => {
    if (!repoRoot() || !branchName())
      return ''
    const parentDir = repoRoot().replace(LAST_PATH_SEGMENT_RE, '')
    return `${parentDir}/${repoDirName()}-worktrees/${branchName()}`
  }

  const localBranches = createMemo(() => branches().filter(b => !b.isRemote))
  const remoteBranches = createMemo(() => branches().filter(b => b.isRemote))
  const localBranchNames = createMemo(() => new Set(localBranches().map(b => b.name)))

  // Warn when a remote branch is selected but a local branch with the same name already exists.
  const checkoutBranchWarning = createMemo(() => {
    const selected = selectedCheckoutBranch()
    if (!selected)
      return null
    const entry = branches().find(b => b.name === selected)
    if (!entry?.isRemote)
      return null
    const localName = selected.replace(REMOTE_PREFIX_RE, '')
    if (localBranchNames().has(localName))
      return `Local branch "${localName}" already exists and will be checked out instead.`
    return null
  })

  // Notify parent about visibility changes.
  // `defer: true` skips the initial mount callback. Without it, when the parent
  // switches layout (e.g. Show fallback → main branch) and remounts GitOptions,
  // the new instance immediately fires onVisibilityChange(false), which resets
  // the parent's showGitOptions flag back to false, causing an infinite cycle.
  createEffect(on(
    () => !loading() && showGitOptions(),
    (visible) => {
      props.onVisibilityChange?.(visible)
    },
    { defer: true },
  ))

  // Fetch branches lazily when switch-branch or create-worktree mode is selected.
  let branchGeneration = 0

  const fetchBranches = async () => {
    const gen = ++branchGeneration
    const wid = props.workerId
    if (!wid || !props.selectedPath)
      return

    setBranchesLoading(true)
    try {
      const resp = await workerRpc.listGitBranches(wid, {
        workerId: wid,
        path: props.selectedPath,
        orgId: org.orgId(),
      })
      if (gen !== branchGeneration)
        return
      // Batch branches, selected values, AND loading state together.
      // If branchesLoading is set outside the batch, the <select>
      // children swap (from "Loading..." to branch <option>s) in a
      // separate render pass — the browser resets selectedIndex to 0
      // and SolidJS doesn't re-apply the value because the signal
      // didn't change, only the children did.
      batch(() => {
        setBranches(resp.branches)
        setBranchesLoading(false)
        const cur = resp.currentBranch || currentBranch()
        if (!selectedBaseBranch() && cur) {
          setSelectedBaseBranch(cur)
        }
        if (!selectedNewBranchBase() && cur) {
          setSelectedNewBranchBase(cur)
        }
      })
    }
    catch (err) {
      log.warn('Failed to list git branches', err)
      if (gen === branchGeneration)
        setBranchesLoading(false)
    }
  }

  let worktreeGeneration = 0

  const fetchWorktrees = async () => {
    const gen = ++worktreeGeneration
    const wid = props.workerId
    if (!wid || !props.selectedPath)
      return

    setWorktreesLoading(true)
    try {
      const resp = await workerRpc.listGitWorktrees(wid, {
        workerId: wid,
        path: props.selectedPath,
        orgId: org.orgId(),
      })
      if (gen !== worktreeGeneration)
        return
      // Only show actual worktrees (exclude the main working tree).
      const entries = resp.worktrees
        .filter(wt => !wt.isMain)
        .map(wt => ({ path: wt.path, branch: wt.branch, isMain: wt.isMain }))
      batch(() => {
        setWorktrees(entries)
        setWorktreesLoading(false)
      })
    }
    catch (err) {
      log.warn('Failed to list git worktrees', err)
      if (gen === worktreeGeneration)
        setWorktreesLoading(false)
    }
  }

  // Fetch git info when worker or path changes.
  let gitInfoGeneration = 0
  createEffect(on(() => [props.workerId, props.selectedPath] as const, async ([wid, path]) => {
    const gen = ++gitInfoGeneration
    if (!wid || !path) {
      setIsGitRepo(false)
      setIsRepoRoot(false)
      setIsWorktreeRoot(false)
      setIsDirty(false)
      setGitMode('current')
      return
    }

    // Only show loading state on the first fetch; when switching between
    // git repos keep the previous UI visible to avoid a flash.
    if (!showGitOptions()) {
      setLoading(true)
    }
    try {
      const resp = await workerRpc.getGitInfo(wid, {
        workerId: wid,
        path,
        orgId: org.orgId(),
      })
      if (gen !== gitInfoGeneration)
        return
      batch(() => {
        setIsGitRepo(resp.isGitRepo)
        setIsRepoRoot(resp.isRepoRoot)
        setIsWorktreeRoot(resp.isWorktreeRoot)
        setIsDirty(resp.isDirty)
        setRepoRoot(resp.repoRoot)
        setRepoDirName(resp.repoDirName)
        setCurrentBranch(resp.currentBranch)
        // Reset branch lists but preserve the selected mode.
        setBranches([])
        setWorktrees([])
        setSelectedCheckoutBranch('')
        setSelectedBaseBranch('')
        setSelectedNewBranchBase('')
        setSelectedWorktreePath('')
      })
      // Re-fetch for the current mode since the lists were cleared.
      const mode = gitMode()
      if (mode === 'switch-branch' || mode === 'create-branch' || mode === 'create-worktree') {
        fetchBranches()
      }
      if (mode === 'use-worktree') {
        fetchWorktrees()
      }
    }
    catch (err) {
      log.warn('Failed to get git info', err)
      if (gen !== gitInfoGeneration)
        return
      setIsGitRepo(false)
      setIsRepoRoot(false)
      setIsWorktreeRoot(false)
      setIsDirty(false)
      setGitMode('current')
    }
    finally {
      if (gen === gitInfoGeneration)
        setLoading(false)
    }
  }))

  createEffect(on(() => gitMode(), (mode) => {
    if ((mode === 'switch-branch' || mode === 'create-branch' || mode === 'create-worktree') && branches().length === 0) {
      fetchBranches()
    }
    if (mode === 'use-worktree' && worktrees().length === 0) {
      fetchWorktrees()
    }
  }))

  // Re-fetch branches/worktrees when the refresh button is clicked.
  createEffect(on(() => props.refreshKey, () => {
    const mode = gitMode()
    if (mode === 'switch-branch' || mode === 'create-branch' || mode === 'create-worktree') {
      fetchBranches()
    }
    if (mode === 'use-worktree') {
      fetchWorktrees()
    }
  }, { defer: true }))

  // Notify parent when git options change.
  createEffect(on(
    () => [gitMode(), branchName(), branchError(), selectedCheckoutBranch(), selectedWorktreePath(), selectedBaseBranch(), newBranchName(), newBranchError(), selectedNewBranchBase()] as const,
    ([mode, branch, error, checkout, wtPath, baseBranch, nbName, nbError, nbBase]) => {
      props.onGitModeChange(mode, {
        worktreeBranch: branch,
        worktreeBranchError: error,
        checkoutBranch: checkout,
        useWorktreePath: wtPath,
        worktreeBaseBranch: baseBranch,
        createBranch: nbName,
        createBranchError: nbError,
        createBranchBase: nbBase,
      })
    },
  ))

  const randomizeBranch = () => {
    setBranchName(generateSlug(3, { format: 'kebab' }))
  }

  const randomizeNewBranch = () => {
    setNewBranchName(generateSlug(3, { format: 'kebab' }))
  }

  const BranchSelect = (selectProps: {
    value: Accessor<string>
    setValue: Setter<string>
    showPrompt?: boolean
    showCurrent?: boolean
  }) => (
    <select
      value={selectProps.value()}
      onChange={e => selectProps.setValue(e.currentTarget.value)}
      disabled={branchesLoading()}
    >
      <Show when={branchesLoading()}>
        <option value="">Loading branches...</option>
      </Show>
      <Show when={!branchesLoading() && branches().length === 0}>
        <option value="">No branches found</option>
      </Show>
      <Show when={!branchesLoading() && branches().length > 0}>
        <Show when={selectProps.showPrompt}>
          <option value="">Select a branch...</option>
        </Show>
        <Show when={localBranches().length > 0}>
          <optgroup label="Local">
            <For each={localBranches()}>
              {b => (
                <option value={b.name}>
                  {b.name}
                  {selectProps.showCurrent && b.name === currentBranch() ? ' (current)' : ''}
                </option>
              )}
            </For>
          </optgroup>
        </Show>
        <Show when={remoteBranches().length > 0}>
          <optgroup label="Remote">
            <For each={remoteBranches()}>
              {b => <option value={b.name}>{b.name}</option>}
            </For>
          </optgroup>
        </Show>
      </Show>
    </select>
  )

  return (
    <Show when={!loading() && showGitOptions()}>
      <div class="vstack gap-2">
        <div class={labelRow}>Git options</div>
        <div class={radioGroup}>
          {/* Use current state */}
          <label class={radioRow}>
            <input
              type="radio"
              name="git-mode"
              checked={gitMode() === 'current'}
              onChange={() => setGitMode('current')}
            />
            Use current state
          </label>
          <Show when={gitMode() === 'current' && currentBranch()}>
            <div class={radioSubContent}>
              <div class={pathPreview}>
                {'Currently on branch: '}
                {currentBranch()}
              </div>
            </div>
          </Show>

          {/* Switch to branch */}
          <label class={radioRow}>
            <input
              type="radio"
              name="git-mode"
              checked={gitMode() === 'switch-branch'}
              onChange={() => setGitMode('switch-branch')}
            />
            Switch to branch
          </label>
          <Show when={gitMode() === 'switch-branch'}>
            <div class={radioSubContent}>
              <Show when={isDirty()}>
                <div class={warningText}>
                  The working copy has uncommitted changes. Switching branches may fail or discard changes.
                </div>
              </Show>
              <BranchSelect value={selectedCheckoutBranch} setValue={setSelectedCheckoutBranch} showPrompt />
              <Show when={checkoutBranchWarning()}>
                <div class={warningText}>{checkoutBranchWarning()}</div>
              </Show>
            </div>
          </Show>

          {/* Create new branch */}
          <label class={radioRow}>
            <input
              type="radio"
              name="git-mode"
              checked={gitMode() === 'create-branch'}
              onChange={() => setGitMode('create-branch')}
            />
            Create new branch
          </label>
          <Show when={gitMode() === 'create-branch'}>
            <div class={radioSubContent}>
              <Show when={isDirty()}>
                <div class={warningText}>
                  The working copy has uncommitted changes. Creating a new branch will include them.
                </div>
              </Show>
              <div>
                <div class={labelRow}>
                  Branch Name
                  <RefreshButton onClick={randomizeNewBranch} title="Generate random name" />
                </div>
                <input
                  type="text"
                  value={newBranchName()}
                  onInput={e => setNewBranchName(e.currentTarget.value)}
                  placeholder="feature-branch"
                />
                <Show when={newBranchError()}>
                  <div class={errorText}>{newBranchError()}</div>
                </Show>
              </div>
              <div>
                <div class={labelRow}>Base Branch</div>
                <BranchSelect value={selectedNewBranchBase} setValue={setSelectedNewBranchBase} showCurrent />
              </div>
            </div>
          </Show>

          {/* Create new worktree */}
          <label class={radioRow}>
            <input
              type="radio"
              name="git-mode"
              checked={gitMode() === 'create-worktree'}
              onChange={() => setGitMode('create-worktree')}
            />
            Create new worktree
          </label>
          <Show when={gitMode() === 'create-worktree'}>
            <div class={radioSubContent}>
              <Show when={isDirty()}>
                <div class={warningText}>
                  The selected working copy has uncommitted changes that will not be transferred to the new worktree.
                </div>
              </Show>
              <div>
                <div class={labelRow}>
                  Branch Name
                  <RefreshButton onClick={randomizeBranch} title="Generate random name" />
                </div>
                <input
                  type="text"
                  value={branchName()}
                  onInput={e => setBranchName(e.currentTarget.value)}
                  placeholder="feature-branch"
                />
                <Show when={branchError()}>
                  <div class={errorText}>{branchError()}</div>
                </Show>
              </div>
              <div>
                <div class={labelRow}>Base Branch</div>
                <BranchSelect value={selectedBaseBranch} setValue={setSelectedBaseBranch} showCurrent />
              </div>
              <Show when={worktreePath()}>
                <div class={pathPreview}>
                  Worktree path:
                  {' '}
                  <Tooltip text={worktreePath()}><code>{tildify(worktreePath(), props.homeDir)}</code></Tooltip>
                </div>
              </Show>
            </div>
          </Show>

          {/* Use existing worktree */}
          <label class={radioRow}>
            <input
              type="radio"
              name="git-mode"
              checked={gitMode() === 'use-worktree'}
              onChange={() => setGitMode('use-worktree')}
            />
            Use existing worktree
          </label>
          <Show when={gitMode() === 'use-worktree'}>
            <div class={radioSubContent}>
              <select
                value={selectedWorktreePath()}
                onChange={e => setSelectedWorktreePath(e.currentTarget.value)}
                disabled={worktreesLoading()}
              >
                <Show when={worktreesLoading()}>
                  <option value="">Loading worktrees...</option>
                </Show>
                <Show when={!worktreesLoading() && worktrees().length === 0}>
                  <option value="">No worktrees found</option>
                </Show>
                <Show when={!worktreesLoading() && worktrees().length > 0}>
                  <option value="">Select a worktree...</option>
                  <For each={worktrees()}>
                    {wt => (
                      <option value={wt.path}>
                        {wt.branch ? `${wt.branch} \u2014 ` : ''}
                        {tildify(wt.path, props.homeDir)}
                      </option>
                    )}
                  </For>
                </Show>
              </select>
            </div>
          </Show>
        </div>
      </div>
    </Show>
  )
}
