import type { Component } from 'solid-js'
import type { GitBranchEntry } from '~/generated/leapmux/v1/git_pb'
import type { GitMode } from '~/hooks/createWorkerDialogState'
import { generateSlug } from 'random-word-slugs'
import { createEffect, createMemo, createSignal, For, on, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { tildify } from '~/components/chat/messageUtils'
import { RefreshButton } from '~/components/common/RefreshButton'
import { Tooltip } from '~/components/common/Tooltip'
import { useOrg } from '~/context/OrgContext'
import { validateBranchName } from '~/lib/validate'
import { errorText, labelRow, pathPreview, radioGroup, radioRow, radioSubContent, warningText } from '~/styles/shared.css'

const LAST_PATH_SEGMENT_RE = /\/[^/]+$/

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
    },
  ) => void
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
  const [rootCurrentBranch, setRootCurrentBranch] = createSignal('')
  const [selectedCheckoutBranch, setSelectedCheckoutBranch] = createSignal('')
  const [selectedBaseBranch, setSelectedBaseBranch] = createSignal('')

  // Worktree list for use-worktree mode
  const [worktrees, setWorktrees] = createSignal<{ path: string, branch: string, isMain: boolean }[]>([])
  const [worktreesLoading, setWorktreesLoading] = createSignal(false)
  const [selectedWorktreePath, setSelectedWorktreePath] = createSignal('')

  const branchError = createMemo(() => validateBranchName(branchName()))

  const showGitOptions = () => isGitRepo() && (isRepoRoot() || isWorktreeRoot())

  const worktreePath = () => {
    if (!repoRoot() || !branchName())
      return ''
    const parentDir = repoRoot().replace(LAST_PATH_SEGMENT_RE, '')
    return `${parentDir}/${repoDirName()}-worktrees/${branchName()}`
  }

  const localBranches = createMemo(() => branches().filter(b => !b.isRemote))
  const remoteBranches = createMemo(() => branches().filter(b => b.isRemote))

  // Notify parent about visibility changes.
  createEffect(on(
    () => !loading() && showGitOptions(),
    (visible) => { props.onVisibilityChange?.(visible) },
  ))

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

    setLoading(true)
    try {
      const resp = await workerRpc.getGitInfo(wid, {
        workerId: wid,
        path,
        orgId: org.orgId(),
      })
      if (gen !== gitInfoGeneration)
        return
      setIsGitRepo(resp.isGitRepo)
      setIsRepoRoot(resp.isRepoRoot)
      setIsWorktreeRoot(resp.isWorktreeRoot)
      setIsDirty(resp.isDirty)
      setRepoRoot(resp.repoRoot)
      setRepoDirName(resp.repoDirName)
      setCurrentBranch(resp.currentBranch)
      // Reset to default mode on path change.
      setGitMode('current')
      // Reset branch lists.
      setBranches([])
      setWorktrees([])
      setSelectedCheckoutBranch('')
      setSelectedWorktreePath('')
    }
    catch {
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
      setBranches(resp.branches)
      setRootCurrentBranch(resp.currentBranch)
      // Default the base branch to the root repo's current branch.
      if (!selectedBaseBranch() && resp.currentBranch) {
        setSelectedBaseBranch(resp.currentBranch)
      }
    }
    catch {}
    finally {
      if (gen === branchGeneration)
        setBranchesLoading(false)
    }
  }

  const fetchWorktrees = async () => {
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
      // Filter out the main working tree if it equals the selected directory.
      const entries = resp.worktrees
        .filter(wt => !(wt.isMain && wt.path === props.selectedPath))
        .map(wt => ({ path: wt.path, branch: wt.branch, isMain: wt.isMain }))
      setWorktrees(entries)
    }
    catch {}
    finally {
      setWorktreesLoading(false)
    }
  }

  createEffect(on(() => gitMode(), (mode) => {
    if ((mode === 'switch-branch' || mode === 'create-worktree') && branches().length === 0) {
      fetchBranches()
    }
    if (mode === 'use-worktree' && worktrees().length === 0) {
      fetchWorktrees()
    }
  }))

  // Notify parent when git options change.
  createEffect(on(
    () => [gitMode(), branchName(), branchError(), selectedCheckoutBranch(), selectedWorktreePath(), selectedBaseBranch()] as const,
    ([mode, branch, error, checkout, wtPath, baseBranch]) => {
      props.onGitModeChange(mode, {
        worktreeBranch: branch,
        worktreeBranchError: error,
        checkoutBranch: checkout,
        useWorktreePath: wtPath,
        worktreeBaseBranch: baseBranch,
      })
    },
  ))

  const randomizeBranch = () => {
    setBranchName(generateSlug(3, { format: 'kebab' }))
  }

  const handleModeChange = (mode: GitMode) => {
    setGitMode(mode)
  }

  return (
    <Show when={!loading() && showGitOptions()}>
      <div class={radioGroup}>
        {/* Use current state */}
        <label class={radioRow}>
          <input
            type="radio"
            name="git-mode"
            checked={gitMode() === 'current'}
            onChange={() => handleModeChange('current')}
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
            onChange={() => handleModeChange('switch-branch')}
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
            <select
              value={selectedCheckoutBranch()}
              onChange={e => setSelectedCheckoutBranch(e.currentTarget.value)}
              disabled={branchesLoading()}
            >
              <Show when={branchesLoading()}>
                <option value="">Loading branches...</option>
              </Show>
              <Show when={!branchesLoading() && branches().length === 0}>
                <option value="">No branches found</option>
              </Show>
              <Show when={!branchesLoading() && branches().length > 0}>
                <option value="">Select a branch...</option>
                <Show when={localBranches().length > 0}>
                  <optgroup label="Local">
                    <For each={localBranches()}>
                      {b => <option value={b.name}>{b.name}</option>}
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
          </div>
        </Show>

        {/* Create new worktree */}
        <label class={radioRow}>
          <input
            type="radio"
            name="git-mode"
            checked={gitMode() === 'create-worktree'}
            onChange={() => handleModeChange('create-worktree')}
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
              <select
                value={selectedBaseBranch()}
                onChange={e => setSelectedBaseBranch(e.currentTarget.value)}
                disabled={branchesLoading()}
              >
                <Show when={branchesLoading()}>
                  <option value="">Loading branches...</option>
                </Show>
                <Show when={!branchesLoading() && branches().length > 0}>
                  <Show when={localBranches().length > 0}>
                    <optgroup label="Local">
                      <For each={localBranches()}>
                        {b => (
                          <option value={b.name}>
                            {b.name}
                            {b.name === rootCurrentBranch() ? ' (current)' : ''}
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
            onChange={() => handleModeChange('use-worktree')}
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
    </Show>
  )
}
