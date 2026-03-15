import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { createSignal } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'

export { GitFileStatusCode }
export type { GitFileStatusEntry }

export type GitFilterTab = 'all' | 'changed' | 'staged' | 'unstaged'

interface GitFileStatusState {
  isGitRepo: boolean
  repoRoot: string
  originUrl: string
  currentBranch: string
  files: GitFileStatusEntry[]
}

export function createGitFileStatusStore() {
  const [state, setState] = createStore<GitFileStatusState>({
    isGitRepo: false,
    repoRoot: '',
    originUrl: '',
    currentBranch: '',
    files: [],
  })

  const [loading, setLoading] = createSignal(false)

  const refresh = async (workerId: string, path: string) => {
    if (!workerId || !path)
      return
    setLoading(true)
    try {
      const resp = await workerRpc.getGitFileStatus(workerId, { workerId, path })
      setState(produce((s) => {
        s.isGitRepo = true
        s.repoRoot = resp.repoRoot
        s.originUrl = resp.originUrl
        s.currentBranch = resp.currentBranch
        s.files = resp.files
      }))
    }
    catch {
      setState(produce((s) => {
        s.isGitRepo = false
        s.repoRoot = ''
        s.originUrl = ''
        s.currentBranch = ''
        s.files = []
      }))
    }
    finally {
      setLoading(false)
    }
  }

  const clear = () => {
    setState({ isGitRepo: false, repoRoot: '', originUrl: '', currentBranch: '', files: [] })
  }

  const getFileStatus = (absPath: string): GitFileStatusEntry | undefined => {
    // Convert absolute path to relative path from repo root.
    const root = state.repoRoot
    if (!root)
      return undefined
    const relPath = absPath.startsWith(`${root}/`) ? absPath.slice(root.length + 1) : absPath
    return state.files.find(f => f.path === relPath)
  }

  const getChangedFiles = (filter: GitFilterTab): GitFileStatusEntry[] => {
    if (filter === 'all')
      return state.files
    return state.files.filter((f) => {
      if (filter === 'staged') {
        return f.stagedStatus !== GitFileStatusCode.UNSPECIFIED
      }
      if (filter === 'unstaged') {
        return f.unstagedStatus !== GitFileStatusCode.UNSPECIFIED
      }
      // 'changed' — any change (staged or unstaged)
      return f.stagedStatus !== GitFileStatusCode.UNSPECIFIED
        || f.unstagedStatus !== GitFileStatusCode.UNSPECIFIED
    })
  }

  const getDirDiffStats = (dirPath: string): { added: number, deleted: number, untracked: number } => {
    const root = state.repoRoot
    if (!root)
      return { added: 0, deleted: 0, untracked: 0 }
    const isRoot = dirPath === root
    const relDir = dirPath.startsWith(`${root}/`) ? dirPath.slice(root.length + 1) : dirPath
    let added = 0
    let deleted = 0
    let untracked = 0
    for (const f of state.files) {
      if (isRoot || f.path.startsWith(`${relDir}/`) || f.path === relDir) {
        if (f.unstagedStatus === GitFileStatusCode.UNTRACKED) {
          untracked++
        }
        else {
          added += f.linesAdded + f.stagedLinesAdded
          deleted += f.linesDeleted + f.stagedLinesDeleted
        }
      }
    }
    return { added, deleted, untracked }
  }

  const hasChanges = (dirPath: string): boolean => {
    const root = state.repoRoot
    if (!root)
      return false
    const relDir = dirPath.startsWith(`${root}/`) ? dirPath.slice(root.length + 1) : dirPath
    return state.files.some(f => f.path.startsWith(`${relDir}/`) || f.path === relDir)
  }

  return {
    state,
    loading,
    refresh,
    clear,
    getFileStatus,
    getChangedFiles,
    getDirDiffStats,
    hasChanges,
  }
}
