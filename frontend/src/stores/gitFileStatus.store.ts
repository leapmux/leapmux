import type { GitFileStatusEntry } from '~/generated/leapmux/v1/git_pb'
import { createSignal } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import { gitClient } from '~/api/clients'
import { apiCallTimeout } from '~/api/transport'
import { GitFileStatusCode } from '~/generated/leapmux/v1/git_pb'

export { GitFileStatusCode }
export type { GitFileStatusEntry }

export type GitFilterTab = 'all' | 'changed' | 'staged' | 'unstaged'

interface GitFileStatusState {
  isGitRepo: boolean
  repoRoot: string
  files: GitFileStatusEntry[]
}

export function createGitFileStatusStore() {
  const [state, setState] = createStore<GitFileStatusState>({
    isGitRepo: false,
    repoRoot: '',
    files: [],
  })

  const [loading, setLoading] = createSignal(false)
  const [lastFetchedAt, setLastFetchedAt] = createSignal(0)

  const refresh = async (workerId: string, path: string) => {
    if (!workerId || !path)
      return
    setLoading(true)
    try {
      const resp = await gitClient.getGitFileStatus({ workerId, path }, apiCallTimeout())
      setState(produce((s) => {
        s.isGitRepo = true
        s.repoRoot = resp.repoRoot
        s.files = resp.files
      }))
      setLastFetchedAt(Date.now())
    }
    catch {
      setState(produce((s) => {
        s.isGitRepo = false
        s.repoRoot = ''
        s.files = []
      }))
    }
    finally {
      setLoading(false)
    }
  }

  const clear = () => {
    setState({ isGitRepo: false, repoRoot: '', files: [] })
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
    lastFetchedAt,
    refresh,
    clear,
    getFileStatus,
    getChangedFiles,
    hasChanges,
  }
}
