import type { DirectoryTreeHandle } from '~/components/tree/DirectoryTree'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import { createEffect, createSignal, on, onMount } from 'solid-js'
import { workerClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { useOrg } from '~/context/OrgContext'
import { createIdentityCache } from '~/lib/identityCache'
import { createWorkerInfoStore } from '~/stores/workerInfo.store'

export type GitMode = 'current' | 'switch-branch' | 'create-branch' | 'create-worktree' | 'use-worktree'

interface WorkerDialogStateOptions {
  preselectedWorkerId?: string
  defaultWorkingDir?: string
  /** When true, resolves worktree roots to the original repo root on mount. */
  resolveWorktree?: boolean
}

export function createWorkerDialogState(options: WorkerDialogStateOptions = {}) {
  const org = useOrg()
  const workerInfoStore = createWorkerInfoStore()
  // Workers come back as freshly-deserialized proto objects on every
  // listWorkers() call, even when nothing has changed. Stabilize the
  // object identity by id so the dialog's <For> doesn't unmount and
  // remount every row on each refresh.
  const workerIdentity = createIdentityCache<Worker>({
    keyOf: w => w.id,
  })
  const [workers, setWorkers] = createSignal<Worker[]>([])
  const [workerId, setWorkerId] = createSignal('')
  const [workingDir, setWorkingDir] = createSignal(options.defaultWorkingDir ?? '')
  const [error, setError] = createSignal<string | null>(null)
  const [refreshing, setRefreshing] = createSignal(false)
  const [gitMode, setGitMode] = createSignal<GitMode>('current')
  const [worktreeBranch, setWorktreeBranch] = createSignal('')
  const [worktreeBranchError, setWorktreeBranchError] = createSignal<string | null>(null)
  const [checkoutBranch, setCheckoutBranch] = createSignal('')
  const [useWorktreePath, setUseWorktreePath] = createSignal('')
  const [worktreeBaseBranch, setWorktreeBaseBranch] = createSignal('')
  const [createBranch, setCreateBranch] = createSignal('')
  const [createBranchError, setCreateBranchError] = createSignal<string | null>(null)
  const [createBranchBase, setCreateBranchBase] = createSignal('')
  const [showGitOptions, setShowGitOptions] = createSignal(false)
  const [refreshKey, setRefreshKey] = createSignal(0)
  // True while an async worktree-to-repo-root resolution is in flight.
  // GitOptions should not render until this resolves, otherwise it will
  // fetch git info for the (stale) worktree path and show the wrong branch.
  const [worktreeResolving, setWorktreeResolving] = createSignal(
    !!options.resolveWorktree && !!options.defaultWorkingDir,
  )
  let treeHandle: DirectoryTreeHandle | undefined

  const fetchWorkers = async () => {
    try {
      const resp = await workerClient.listWorkers({})
      const online = workerIdentity.stabilize(resp.workers.filter(b => b.online))
      setWorkers(online)
      if (online.length > 0 && !workerId()) {
        setWorkerId(online[0].id)
      }
      // Fetch system info for homeDir via E2EE.
      for (const w of online) {
        workerInfoStore.fetchWorkerInfo(w.id)
      }
      return online.length > 0
    }
    catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load workers')
      return false
    }
  }

  // Fetch on mount only
  onMount(async () => {
    await fetchWorkers()
    // Pre-select worker if specified and online
    if (options.preselectedWorkerId) {
      const match = workers().find(b => b.id === options.preselectedWorkerId)
      if (match) {
        setWorkerId(match.id)
      }
    }
    // Refresh the directory tree to show latest contents.
    treeHandle?.refresh()
  })

  // If the default working directory is a worktree, resolve to the original repo root.
  if (options.resolveWorktree) {
    let resolved = false
    createEffect(on(() => workerId(), async (wid) => {
      if (resolved || !wid || !options.defaultWorkingDir)
        return
      resolved = true
      try {
        const resp = await workerRpc.getGitInfo(wid, {
          workerId: wid,
          path: options.defaultWorkingDir,
          orgId: org.orgId(),
        })
        if (resp.isWorktreeRoot && resp.repoRoot)
          setWorkingDir(resp.repoRoot)
      }
      catch {}
      finally {
        setWorktreeResolving(false)
      }
    }))
  }

  const handleRefresh = async () => {
    setRefreshing(true)
    await fetchWorkers()
    setRefreshing(false)
  }

  const handleGitModeChange = (
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
  ) => {
    setGitMode(mode)
    if (opts.checkoutBranch !== undefined)
      setCheckoutBranch(opts.checkoutBranch)
    if (opts.worktreeBranch !== undefined)
      setWorktreeBranch(opts.worktreeBranch)
    if (opts.worktreeBranchError !== undefined)
      setWorktreeBranchError(opts.worktreeBranchError)
    if (opts.useWorktreePath !== undefined)
      setUseWorktreePath(opts.useWorktreePath)
    if (opts.worktreeBaseBranch !== undefined)
      setWorktreeBaseBranch(opts.worktreeBaseBranch)
    if (opts.createBranch !== undefined)
      setCreateBranch(opts.createBranch)
    if (opts.createBranchError !== undefined)
      setCreateBranchError(opts.createBranchError)
    if (opts.createBranchBase !== undefined)
      setCreateBranchBase(opts.createBranchBase)
  }

  return {
    org,
    workerInfoStore,
    workers,
    workerId,
    setWorkerId,
    workingDir,
    setWorkingDir,
    error,
    setError,
    refreshing,
    handleRefresh,
    gitMode,
    worktreeBranch,
    worktreeBranchError,
    checkoutBranch,
    useWorktreePath,
    worktreeBaseBranch,
    createBranch,
    createBranchError,
    createBranchBase,
    worktreeResolving,
    showGitOptions,
    setShowGitOptions,
    handleGitModeChange,
    refreshKey,
    treeRef: (h: DirectoryTreeHandle) => { treeHandle = h },
    refreshTree: () => {
      treeHandle?.refresh()
      setRefreshKey(k => k + 1)
    },
  }
}

export type WorkerDialogState = ReturnType<typeof createWorkerDialogState>
