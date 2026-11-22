import type { Accessor } from 'solid-js'
import type { TreeNodeData } from './directoryTreeState'
import type { PathFlavor } from '~/lib/paths'
import { batch, createEffect, createMemo, createSelector, createSignal, on, onCleanup, untrack } from 'solid-js'
import { relativeUnder } from '~/lib/paths'
import { isDescendantPath, samePath } from './directoryTreeState'

function splitPathSegments(p: string, flavor: PathFlavor): string[] {
  const parts = flavor === 'win32'
    ? p.split(/[\\/]+/)
    : p.split(/\/+/)
  return parts.filter(Boolean)
}

// Match an intermediate directory collapsed into a merged single-child row (for
// example `displayName: 'src/main/java'`, `path: '/repo/src/main/java'` when
// revealing `/repo/src`). Requiring `0 < tailSegments < displaySegments`
// guarantees `target` lies strictly inside `entry`'s merged prefix, so a
// symlink whose canonical path is an ancestor of the tree root can never match
// an unmerged or unrelated child directory.
function isMergedDirectoryTarget(entry: TreeNodeData, target: string, flavor: PathFlavor): boolean {
  const displaySegments = splitPathSegments(entry.displayName, flavor)
  if (displaySegments.length <= 1)
    return false
  const rel = relativeUnder(entry.path, target, flavor)
  if (!rel)
    return false
  const tailSegments = splitPathSegments(rel, flavor)
  return tailSegments.length > 0 && tailSegments.length < displaySegments.length
}

/**
 * Request to expand, scroll to, focus, and highlight a directory row in
 * `DirectoryTree` (used when activating a local directory link in Markdown).
 */
export interface DirectoryRevealRequest {
  /** Target directory path in the tree root's logical spelling. */
  path: string
  /** Optional canonicalized path from StatFileResponse for symlink/macOS child matching. */
  canonicalPath?: string
  /** Monotonic counter so repeated clicks on the same directory re-trigger focus and cue. */
  nonce: number
}

export interface PendingRevealCue {
  path: string
  nonce: number
  restartCue: boolean
}

export interface ActiveRevealCue {
  path: string
  nonce: number
}

/** Scroll `row` into view inside `container` when it lies above or below the visible viewport. */
export function scrollRowIntoView(container: HTMLElement | undefined, row: HTMLElement): void {
  if (!container)
    return
  const containerRect = container.getBoundingClientRect()
  const rowRect = row.getBoundingClientRect()
  if (rowRect.top < containerRect.top || rowRect.bottom > containerRect.bottom)
    container.scrollTop += rowRect.top - containerRect.top
}

export interface DirectoryRevealDeps {
  workerId: () => string
  rootPath: Accessor<string>
  flavor: Accessor<PathFlavor>
  enabled: () => boolean
  request: () => DirectoryRevealRequest | undefined
  selectedPath: () => string
  showHidden: Accessor<boolean>
  isVisible: () => ((path: string) => boolean) | undefined
  cueDurationMs: number
  getScrollContainer: () => HTMLElement | undefined
  getChildren: (path: string) => TreeNodeData[] | undefined
  unreadableReason: (path: string) => string | undefined
  ensureChildren: (path: string) => Promise<void>
  isNodeExpanded: (path: string) => boolean
  setNodeExpanded: (path: string, expanded: boolean) => void
  onSelect: (path: string) => void
  setFocusedPath: (path: string) => void
  onHandled: (nonce: number) => void
  onRevealHiddenRequired: () => (() => void) | undefined
  onClearFilterRequired: () => (() => void) | undefined
}

export interface DirectoryRevealController {
  pendingCue: Accessor<PendingRevealCue | undefined>
  activeCue: Accessor<ActiveRevealCue | undefined>
  isPendingCuePath: (path: string) => boolean
  isActiveCuePath: (path: string) => boolean
  consumeCue: (path: string, el: HTMLElement) => void
  settlePending: (nonce: number) => void
  cancel: () => void
}

interface WalkMatch {
  resolvedPath: string
  resolvedParent: string | undefined
  expandedAncestors: string[]
  cannotRender: boolean
}

/**
 * Own the async directory-reveal state machine for `DirectoryTree`: expand
 * ancestors, scroll to and focus the resolved directory row, pulse the visual
 * cue, and guarantee `onHandled` settles once on every completion, cancellation,
 * or error exit path.
 */
export function createDirectoryReveal(deps: DirectoryRevealDeps): DirectoryRevealController {
  const [pendingCue, setPendingCue] = createSignal<PendingRevealCue | undefined>(undefined)
  const [activeCue, setActiveCue] = createSignal<ActiveRevealCue | undefined>(undefined)
  const isPendingCuePath = createSelector<string | undefined, string>(() => pendingCue()?.path)
  const isActiveCuePath = createSelector<string | undefined, string>(() => activeCue()?.path)

  let revealVersion = 0
  let activeRevealNonce: number | undefined
  let revealCueTimer: ReturnType<typeof setTimeout> | undefined

  const clearRevealCueTimer = () => {
    if (revealCueTimer !== undefined) {
      clearTimeout(revealCueTimer)
      revealCueTimer = undefined
    }
  }

  const clearActiveCue = () => {
    clearRevealCueTimer()
    setActiveCue(undefined)
  }

  // Unified reveal settlement: clears pending state and reports the unsettled
  // nonce (unless it matches `keepNonce`) so no exit path drops or double-fires
  // `onHandled`.
  const clearPendingState = (keepNonce?: number) => {
    const prevNonce = untrack(pendingCue)?.nonce ?? activeRevealNonce
    if (keepNonce !== undefined && prevNonce === keepNonce)
      return
    setPendingCue(undefined)
    if (keepNonce === undefined)
      activeRevealNonce = undefined
    if (prevNonce !== undefined)
      deps.onHandled(prevNonce)
  }

  const settlePending = (nonce: number) => {
    const pending = untrack(pendingCue)
    if (pending?.nonce === nonce)
      setPendingCue(undefined)
    if (activeRevealNonce === nonce)
      activeRevealNonce = undefined
    deps.onHandled(nonce)
  }

  const cancel = () => {
    ++revealVersion
    clearPendingState()
    clearActiveCue()
  }

  const consumeCue = (path: string, el: HTMLElement) => {
    const pending = untrack(pendingCue)
    if (!pending || pending.path !== path || !el.isConnected)
      return
    const { nonce, restartCue } = pending
    // Clear pending state and activeRevealNonce before notifying the caller so
    // clearing props.directoryReveal neither double-calls onHandled nor cuts
    // short the active visual cue.
    settlePending(nonce)

    scrollRowIntoView(deps.getScrollContainer(), el)
    deps.setFocusedPath(path)
    el.focus()

    // Because clearActiveCue() ran in the reveal trigger effect before Solid
    // flushed render updates and ran this row consumption effect, the cue class
    // is already absent from the row. Force style resolution before re-applying
    // so repeating the cue restarts the animation.
    if (restartCue)
      void el.offsetWidth

    clearRevealCueTimer()
    setActiveCue({ path, nonce })
    revealCueTimer = setTimeout(() => {
      revealCueTimer = undefined
      setActiveCue(prev => (prev?.nonce === nonce
        ? undefined
        : prev))
    }, deps.cueDurationMs)
  }

  const loadDirectory = async (dirPath: string, version: number): Promise<boolean> => {
    if (untrack(() => deps.getChildren(dirPath)) === undefined && !untrack(() => deps.unreadableReason(dirPath))) {
      await deps.ensureChildren(dirPath)
      if (version !== revealVersion)
        return false
    }
    return !untrack(() => deps.unreadableReason(dirPath)) && untrack(() => deps.getChildren(dirPath)) !== undefined
  }

  // Walk a single target spelling (`req.path` first; `req.canonicalPath` only as
  // fallback when the logical path is not present in canonicalized listings).
  // Leaf matching prefers exact `samePath` and falls back only to merged
  // single-child rows whose collapsed segments strictly contain `target`, so
  // symlinks pointing to an ancestor or repo root never hijack sibling rows.
  const walkSingleTarget = async (
    target: string,
    root: string,
    flavor: PathFlavor,
    version: number,
  ): Promise<WalkMatch | 'cancelled' | undefined> => {
    if (samePath(root, target, flavor)) {
      return {
        resolvedPath: root,
        resolvedParent: undefined,
        expandedAncestors: [],
        cannotRender: false,
      }
    }

    let cannotRender = false
    const ensureEntryVisible = (entry: TreeNodeData) => {
      if (entry.hidden && !untrack(deps.showHidden)) {
        const onRevealHidden = deps.onRevealHiddenRequired()
        if (onRevealHidden)
          onRevealHidden()
        else
          cannotRender = true
      }
      const isVis = deps.isVisible()
      if (isVis && !isVis(entry.path)) {
        const onClearFilter = deps.onClearFilterRequired()
        if (onClearFilter)
          onClearFilter()
        else
          cannotRender = true
      }
    }

    let current = root
    const expandedAncestors: string[] = []
    const pendingExpands: string[] = []
    const flushExpands = () => {
      if (pendingExpands.length === 0)
        return
      const toExpand = pendingExpands.splice(0)
      batch(() => {
        for (const p of toExpand)
          deps.setNodeExpanded(p, true)
      })
    }

    for (let depth = 0; depth < 64; depth++) {
      let children = untrack(() => deps.getChildren(current))
      if (children === undefined) {
        flushExpands()
        await deps.ensureChildren(current)
        if (version !== revealVersion)
          return 'cancelled'
        children = untrack(() => deps.getChildren(current))
      }
      if (!children)
        break

      const dirs = children.filter(c => c.isDir)
      const matchedLeaf = dirs.find(c => samePath(c.path, target, flavor))
        ?? dirs.find(c => isMergedDirectoryTarget(c, target, flavor))
      if (matchedLeaf) {
        flushExpands()
        ensureEntryVisible(matchedLeaf)
        return {
          resolvedPath: matchedLeaf.path,
          resolvedParent: current,
          expandedAncestors,
          cannotRender,
        }
      }

      const ancestor = dirs.find(c => isDescendantPath(target, c.path, flavor))
      if (!ancestor)
        break

      ensureEntryVisible(ancestor)
      const alreadyLoaded = untrack(() => deps.getChildren(ancestor.path)) !== undefined
      if (!alreadyLoaded)
        flushExpands()
      const loaded = await loadDirectory(ancestor.path, version)
      if (version !== revealVersion)
        return 'cancelled'
      if (!loaded)
        break
      expandedAncestors.push(ancestor.path)
      pendingExpands.push(ancestor.path)
      current = ancestor.path
    }

    flushExpands()
    return undefined
  }

  const runRevealWalk = async (
    req: DirectoryRevealRequest,
    version: number,
    prevCuePath: string | undefined,
  ) => {
    const flavor = untrack(deps.flavor)
    const root = untrack(deps.rootPath)

    // Pass 1: logical `req.path` so symlinks to repo root or internal ancestors
    // resolve to the symlink row itself.
    let match = await walkSingleTarget(req.path, root, flavor, version)
    if (match === 'cancelled' || version !== revealVersion)
      return

    // Pass 2: fallback to `req.canonicalPath` for canonicalized listings (e.g.
    // macOS `/tmp` -> `/private/tmp`) when the logical path did not match.
    if (!match && req.canonicalPath && !samePath(req.path, req.canonicalPath, flavor)) {
      match = await walkSingleTarget(req.canonicalPath, root, flavor, version)
      if (match === 'cancelled' || version !== revealVersion)
        return
    }

    if (!match) {
      settlePending(req.nonce)
      return
    }

    const { resolvedPath, resolvedParent, expandedAncestors, cannotRender } = match

    let leafLoaded = false
    if (resolvedPath !== root) {
      leafLoaded = await loadDirectory(resolvedPath, version)
      if (version !== revealVersion)
        return
    }

    // Re-verify after all awaits that every expanded ancestor is still expanded
    // and the target still exists in its parent's listing; otherwise settle now
    // rather than stranding `pendingCue` on an unmounted row.
    if (resolvedPath !== root) {
      const ancestorsStillExpanded = expandedAncestors.every(p => untrack(() => deps.isNodeExpanded(p)))
      const parentChildren = resolvedParent !== undefined
        ? untrack(() => deps.getChildren(resolvedParent))
        : undefined
      const stillPresent = parentChildren?.some(c => c.isDir && samePath(c.path, resolvedPath, flavor)) ?? false
      if (!ancestorsStillExpanded || !stillPresent) {
        settlePending(req.nonce)
        return
      }
    }

    batch(() => {
      if (resolvedPath !== root && leafLoaded)
        deps.setNodeExpanded(resolvedPath, true)
      if (!samePath(untrack(deps.selectedPath), resolvedPath, flavor))
        deps.onSelect(resolvedPath)
      if (cannotRender) {
        settlePending(req.nonce)
      }
      else {
        setPendingCue({
          path: resolvedPath,
          nonce: req.nonce,
          restartCue: prevCuePath !== undefined && samePath(prevCuePath, resolvedPath, flavor),
        })
      }
    })
  }

  // Memoize by value so dynamic JSX spreads on `<DirectoryTree>` (such as
  // `FilesSection`'s `{...isVisibleProps()}`) do not spuriously re-fire reveal
  // cleanup or restart a reveal when an unrelated spread prop toggles.
  const revealScopeKey = createMemo(() => `${deps.workerId()}\0${deps.rootPath()}`)
  const revealTrigger = createMemo(
    () => ({
      req: deps.request(),
      enabled: deps.enabled(),
    }),
    undefined,
    {
      equals: (a, b) => a.req === b.req && a.enabled === b.enabled,
    },
  )

  // Registered BEFORE the reveal trigger effect so a simultaneous
  // `[workerId, rootPath()]` + `directoryReveal` update clears only the prior
  // reveal without cancelling the new one, while an intra-workspace switch that
  // interrupts an in-flight walk or `!enabled` hold clears the stale request.
  createEffect(on(
    revealScopeKey,
    () => {
      cancel()
    },
    { defer: true },
  ))
  onCleanup(cancel)

  // Scoped via `on(...)` so reactive reads during a cached walk and synchronous
  // callbacks (`onRevealHiddenRequired`, `onClearFilterRequired`, `onSelect`)
  // never re-trigger the reveal mid-walk or cancel `pendingCue`.
  createEffect(on(
    revealTrigger,
    ({ req, enabled }) => {
      const version = ++revealVersion

      if (!req) {
        clearPendingState()
        return
      }

      if (!enabled) {
        activeRevealNonce = req.nonce
        setPendingCue(undefined)
        return
      }

      clearPendingState(req.nonce)
      activeRevealNonce = req.nonce

      const prevCuePath = untrack(activeCue)?.path
      clearActiveCue()

      void runRevealWalk(req, version, prevCuePath)
    },
  ))

  return {
    pendingCue,
    activeCue,
    isPendingCuePath,
    isActiveCuePath,
    consumeCue,
    settlePending,
    cancel,
  }
}
