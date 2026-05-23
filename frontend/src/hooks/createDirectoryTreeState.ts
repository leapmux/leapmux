import type { DirectoryTreeHandle } from '~/components/tree/DirectoryTree'
import { createSignal } from 'solid-js'

/**
 * Tree-state bundle for dialogs that render a `DirectorySelector`. Owns
 * the `DirectoryTreeHandle` ref plus a monotonically-incrementing
 * `treeKey` so consumers (e.g. `GitOptions.refreshKey`) can re-fetch
 * branch/worktree lists in lockstep with the tree refresh.
 *
 * Kept separate from `createWorkerDialogContext` so dialogs that don't
 * render a directory tree (ChangeBranchDialog, DeleteBranchDialog) don't
 * carry tree state through their public surface. The 3 dialogs that do
 * render a tree pair this with the base state explicitly.
 */
export function createDirectoryTreeState() {
  const [treeKey, setTreeKey] = createSignal(0)
  let treeHandle: DirectoryTreeHandle | undefined
  const setTreeRef = (h: DirectoryTreeHandle) => {
    treeHandle = h
  }
  const refreshTree = () => {
    treeHandle?.refresh()
    setTreeKey(k => k + 1)
  }
  return { treeKey, setTreeRef, refreshTree }
}

export type DirectoryTreeState = ReturnType<typeof createDirectoryTreeState>
