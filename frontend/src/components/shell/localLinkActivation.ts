import type { PathFlavor } from '~/lib/paths'
import { Code } from '@connectrpc/connect'
import { ChannelError } from '~/lib/channelError'
import { resolveLocalLinkCandidates } from '~/lib/localLinks'
import { lruSet } from '~/lib/mapLru'
import { join, normalizeSeparators, parentDirectory, relativeUnder } from '~/lib/paths'

const MAX_CANONICAL_ROOT_CACHE_ENTRIES = 64

export interface LocalLinkActivationSource {
  workerId: string
  baseDir: string
  workingDir: string
  homeDir: string
  flavor: PathFlavor
  /** Bring the originating tile/tab into focus before opening or revealing. */
  activateSource?: () => void
}

export interface LocalLinkActivationDeps {
  statFile: (workerId: string, req: { path: string }) => Promise<{
    info?: { path: string, isDir: boolean } | undefined
    canonicalPath?: string | undefined
  }>
  openFile: (path: string, at: { workerId: string, workingDir: string }) => void
  revealDirectory: (target: { path: string, canonicalPath?: string }) => void
  showWarning: (message: string, cause?: unknown) => void
}

function isRetryableStatError(err: unknown): boolean {
  return err instanceof ChannelError
    && err.source === 'rpc'
    && (err.code === Code.NotFound || err.code === Code.PermissionDenied)
}

/**
 * Coordinate activating a local filesystem link or local image from rendered
 * Markdown: focus the source tile, probe candidate paths via `StatFile`, and
 * either open a File Viewer tab or reveal the directory inside the active
 * `Files` tree.
 */
export function createLocalLinkActivator(deps: LocalLinkActivationDeps): {
  activate: (rawHref: string, source: LocalLinkActivationSource) => Promise<void>
  cancelPending: () => void
  clearCanonicalRootCache: () => void
} {
  let activationSeq = 0
  const canonicalRootCache = new Map<string, string>()

  const cancelPending = () => {
    activationSeq += 1
  }

  const clearCanonicalRootCache = () => {
    canonicalRootCache.clear()
  }

  const activate = async (rawHref: string, source: LocalLinkActivationSource): Promise<void> => {
    // Focus the originating tile/tab synchronously before any await so
    // `followActiveTabSelection` settles on the source tab before any later
    // `setFileTreePath`, and so `handleFileOpen` opens inside the right tile.
    source.activateSource?.()

    const candidates = resolveLocalLinkCandidates(rawHref, {
      baseDir: source.baseDir,
      workingDir: source.workingDir,
      homeDir: source.homeDir,
      flavor: source.flavor,
    })
    if (!source.workerId || candidates.length === 0) {
      deps.showWarning(`Cannot resolve local path: ${rawHref}`)
      return
    }

    const seq = ++activationSeq
    let matched: {
      info: { path: string, isDir: boolean }
      canonicalPath: string
      matchedCandidate: string
    } | undefined
    let lastError: unknown

    for (const candidate of candidates) {
      try {
        const resp = await deps.statFile(source.workerId, { path: candidate })
        if (seq !== activationSeq)
          return
        if (resp.info) {
          matched = {
            info: resp.info,
            canonicalPath: resp.canonicalPath ?? '',
            matchedCandidate: candidate,
          }
          break
        }
        lastError = new Error('Worker returned empty file metadata')
        break
      }
      catch (err) {
        if (seq !== activationSeq)
          return
        lastError = err
        if (!isRetryableStatError(err))
          break
      }
    }

    if (seq !== activationSeq)
      return

    if (!matched) {
      if (lastError instanceof ChannelError && lastError.source === 'rpc' && lastError.message) {
        deps.showWarning(`Cannot open local link: ${lastError.message}`, lastError)
      }
      else {
        deps.showWarning('Cannot open local link', lastError)
      }
      return
    }

    const flavor = source.flavor
    if (!matched.info.isDir) {
      const filePath = matched.info.path || matched.matchedCandidate
      const workingDir = source.workingDir || parentDirectory(filePath, flavor)
      deps.openFile(filePath, {
        workerId: source.workerId,
        workingDir,
      })
      return
    }

    const logicalPath = matched.info.path || matched.matchedCandidate
    const canonicalPath = matched.canonicalPath
    const treeRoot = source.workingDir || source.homeDir
    if (!treeRoot) {
      deps.showWarning(`Directory is outside the current file tree: ${logicalPath}`)
      return
    }

    const norm = (p: string) => normalizeSeparators(p, flavor)
    const normRoot = norm(treeRoot)
    let treePath: string | undefined

    if (relativeUnder(norm(logicalPath), normRoot, flavor) !== null) {
      treePath = logicalPath
    }
    else if (canonicalPath && relativeUnder(norm(canonicalPath), normRoot, flavor) !== null) {
      treePath = canonicalPath
    }
    else {
      const cacheKey = `${source.workerId}\0${treeRoot}`
      let rootCanonical = canonicalRootCache.get(cacheKey)
      if (rootCanonical === undefined) {
        try {
          const rootResp = await deps.statFile(source.workerId, { path: treeRoot })
          if (seq !== activationSeq)
            return
          if (rootResp.canonicalPath) {
            rootCanonical = rootResp.canonicalPath
            lruSet(canonicalRootCache, cacheKey, rootCanonical, MAX_CANONICAL_ROOT_CACHE_ENTRIES)
          }
        }
        catch {
          if (seq !== activationSeq)
            return
        }
      }
      else {
        // Refresh recency on cache hit.
        lruSet(canonicalRootCache, cacheKey, rootCanonical, MAX_CANONICAL_ROOT_CACHE_ENTRIES)
      }

      if (rootCanonical) {
        const normRootCanonical = norm(rootCanonical)
        const rem = (canonicalPath
          ? relativeUnder(norm(canonicalPath), normRootCanonical, flavor)
          : null) ?? relativeUnder(norm(logicalPath), normRootCanonical, flavor)
        if (rem !== null) {
          treePath = rem === ''
            ? treeRoot
            : join([treeRoot, rem], flavor)
        }
      }
    }

    if (treePath !== undefined) {
      deps.revealDirectory({
        path: treePath,
        ...(canonicalPath
          ? { canonicalPath }
          : {}),
      })
    }
    else {
      deps.showWarning(`Directory is outside the current file tree: ${logicalPath}`)
    }
  }

  return {
    activate,
    cancelPending,
    clearCanonicalRootCache,
  }
}
