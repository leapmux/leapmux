import type { Component } from 'solid-js'
import { generateSlug } from 'random-word-slugs'
import { createEffect, createMemo, createSignal, on, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { tildify } from '~/components/chat/messageUtils'
import { RefreshButton } from '~/components/common/RefreshButton'
import { useOrg } from '~/context/OrgContext'
import { validateBranchName } from '~/lib/validate'
import { checkboxRow, errorText, labelRow, pathPreview, warningText } from '~/styles/shared.css'

interface WorktreeOptionsProps {
  workerId: string
  selectedPath: string
  homeDir?: string
  onWorktreeChange: (create: boolean, branch: string, branchError: string | null) => void
}

export const WorktreeOptions: Component<WorktreeOptionsProps> = (props) => {
  const org = useOrg()
  const [isGitRepo, setIsGitRepo] = createSignal(false)
  const [isRepoRoot, setIsRepoRoot] = createSignal(false)
  const [isWorktreeRoot, setIsWorktreeRoot] = createSignal(false)
  const [isDirty, setIsDirty] = createSignal(false)
  const [repoRoot, setRepoRoot] = createSignal('')
  const [repoDirName, setRepoDirName] = createSignal('')
  const [createWorktree, setCreateWorktree] = createSignal(false)
  const [branchName, setBranchName] = createSignal(generateSlug(3, { format: 'kebab' }))
  const [loading, setLoading] = createSignal(false)
  // Once the user explicitly unchecks, stop auto-checking for this dialog session.
  let userUnchecked = false
  const branchError = createMemo(() => validateBranchName(branchName()))

  const showWorktreeOption = () => isGitRepo() && (isRepoRoot() || isWorktreeRoot())

  const worktreePath = () => {
    if (!repoRoot() || !branchName())
      return ''
    const parentDir = repoRoot().replace(/\/[^/]+$/, '')
    return `${parentDir}/${repoDirName()}-worktrees/${branchName()}`
  }

  // Fetch git info when worker or path changes.
  // Use a generation counter to discard stale responses from previous
  // invocations (the async RPC may return out-of-order when the path
  // changes rapidly).
  let gitInfoGeneration = 0
  createEffect(on(() => [props.workerId, props.selectedPath] as const, async ([wid, path]) => {
    const gen = ++gitInfoGeneration
    if (!wid || !path) {
      setIsGitRepo(false)
      setIsRepoRoot(false)
      setIsWorktreeRoot(false)
      setIsDirty(false)
      setCreateWorktree(false)
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
        return // Stale response; a newer request is in flight.
      setIsGitRepo(resp.isGitRepo)
      setIsRepoRoot(resp.isRepoRoot)
      setIsWorktreeRoot(resp.isWorktreeRoot)
      setIsDirty(resp.isDirty)
      setRepoRoot(resp.repoRoot)
      setRepoDirName(resp.repoDirName)
      // Auto-check only if the user hasn't explicitly unchecked.
      if (!userUnchecked) {
        if (resp.isGitRepo && (resp.isRepoRoot || resp.isWorktreeRoot)) {
          setCreateWorktree(true)
        }
        else {
          setCreateWorktree(false)
        }
      }
    }
    catch {
      if (gen !== gitInfoGeneration)
        return
      setIsGitRepo(false)
      setIsRepoRoot(false)
      setIsWorktreeRoot(false)
      setIsDirty(false)
      setCreateWorktree(false)
    }
    finally {
      if (gen === gitInfoGeneration)
        setLoading(false)
    }
  }))

  // Notify parent when worktree options change.
  createEffect(on(() => [createWorktree(), branchName(), branchError()] as const, ([create, branch, error]) => {
    props.onWorktreeChange(create, branch, error)
  }))

  const randomizeBranch = () => {
    setBranchName(generateSlug(3, { format: 'kebab' }))
  }

  return (
    <Show when={!loading() && showWorktreeOption()}>
      <label class={checkboxRow}>
        <input
          type="checkbox"
          checked={createWorktree()}
          onChange={(e) => {
            const checked = e.currentTarget.checked
            if (!checked)
              userUnchecked = true
            setCreateWorktree(checked)
          }}
        />
        Create new worktree
      </label>
      <Show when={createWorktree()}>
        <Show when={isDirty()}>
          <div class={warningText}>
            The selected working copy has uncommitted changes that will not be transferred to the new worktree.
          </div>
        </Show>
        <label>
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
        </label>
        <Show when={worktreePath()}>
          <div class={pathPreview}>
            Worktree path:
            {' '}
            <code title={worktreePath()}>{tildify(worktreePath(), props.homeDir)}</code>
          </div>
        </Show>
      </Show>
    </Show>
  )
}
