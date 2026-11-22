import type { LocalLinkResolveOptions } from '~/lib/localLinks'
import { resolveLocalLinkCandidates } from '~/lib/localLinks'
import { isLocalPathFallbackRpcError, localPathRpcErrorMessage } from '~/lib/localPathRpc'
import { lruGet, lruSet } from '~/lib/mapLru'
import { join, parentDirectory, relativeUnder } from '~/lib/paths'

const MAX_CANONICAL_ROOT_CACHE_ENTRIES = 64

export interface LocalLinkActivationSource extends LocalLinkResolveOptions {
  workerId: string
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

interface StatMatch {
  info: { path: string, isDir: boolean }
  canonicalPath: string
  matchedCandidate: string
}

type ProbeStatResult
  = | { kind: 'ok', match: StatMatch }
    | { kind: 'error', error: unknown }

async function probeStatCandidates(
  statFile: LocalLinkActivationDeps['statFile'],
  workerId: string,
  candidates: readonly string[],
): Promise<ProbeStatResult> {
  const settled = candidates.length === 1
    ? [await statFile(workerId, { path: candidates[0]! }).then(
        value => ({ status: 'fulfilled' as const, value }),
        reason => ({ status: 'rejected' as const, reason }),
      )]
    : await Promise.allSettled(
        candidates.map(candidate => statFile(workerId, { path: candidate })),
      )

  let lastError: unknown
  for (let i = 0; i < candidates.length; i++) {
    const candidate = candidates[i]!
    const outcome = settled[i]!
    if (outcome.status === 'fulfilled') {
      const resp = outcome.value
      if (resp.info) {
        return {
          kind: 'ok',
          match: {
            info: resp.info,
            canonicalPath: resp.canonicalPath ?? '',
            matchedCandidate: candidate,
          },
        }
      }
      lastError = new Error('Worker returned empty file metadata')
      break
    }
    lastError = outcome.reason
    if (!isLocalPathFallbackRpcError(outcome.reason))
      break
  }

  return { kind: 'error', error: lastError }
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
  let canonicalRootCacheEpoch = 0
  const canonicalRootCache = new Map<string, string>()

  const cancelPending = () => {
    activationSeq += 1
  }

  const clearCanonicalRootCache = () => {
    canonicalRootCacheEpoch += 1
    canonicalRootCache.clear()
  }

  const resolveTreeDirectoryPath = async (
    seq: number,
    source: LocalLinkActivationSource,
    logicalPath: string,
    canonicalPath: string,
    treeRoot: string,
  ): Promise<{ cancelled: true } | { cancelled: false, treePath: string | undefined }> => {
    const flavor = source.flavor

    if (relativeUnder(logicalPath, treeRoot, flavor) !== null)
      return { cancelled: false, treePath: logicalPath }
    if (canonicalPath && relativeUnder(canonicalPath, treeRoot, flavor) !== null)
      return { cancelled: false, treePath: canonicalPath }

    const cacheKey = `${source.workerId}\0${treeRoot}`
    let rootCanonical = lruGet(canonicalRootCache, cacheKey)
    if (rootCanonical === undefined) {
      const epoch = canonicalRootCacheEpoch
      try {
        const rootResp = await deps.statFile(source.workerId, { path: treeRoot })
        if (seq !== activationSeq)
          return { cancelled: true }
        if (rootResp.canonicalPath) {
          rootCanonical = rootResp.canonicalPath
          if (epoch === canonicalRootCacheEpoch)
            lruSet(canonicalRootCache, cacheKey, rootCanonical, MAX_CANONICAL_ROOT_CACHE_ENTRIES)
        }
      }
      catch {
        if (seq !== activationSeq)
          return { cancelled: true }
      }
    }

    if (rootCanonical) {
      const rem = (canonicalPath
        ? relativeUnder(canonicalPath, rootCanonical, flavor)
        : null) ?? relativeUnder(logicalPath, rootCanonical, flavor)
      if (rem !== null) {
        return {
          cancelled: false,
          treePath: rem === ''
            ? treeRoot
            : join([treeRoot, rem], flavor),
        }
      }
    }

    return { cancelled: false, treePath: undefined }
  }

  const activate = async (rawHref: string, source: LocalLinkActivationSource): Promise<void> => {
    // Focus the originating tile/tab synchronously before any await so
    // `followActiveTabSelection` settles on the source tab before any later
    // `setFileTreePath`, and so `handleFileOpen` opens inside the right tile.
    source.activateSource?.()

    // Increment before the unresolvable early return so a failed click on a
    // newly focused tile supersedes any earlier in-flight activation.
    const seq = ++activationSeq

    const candidates = resolveLocalLinkCandidates(rawHref, source)
    if (!source.workerId || candidates.length === 0) {
      deps.showWarning(`Cannot resolve local path: ${rawHref}`)
      return
    }

    const probe = await probeStatCandidates(deps.statFile, source.workerId, candidates)
    if (seq !== activationSeq)
      return

    if (probe.kind === 'error') {
      const rpcMsg = localPathRpcErrorMessage(probe.error)
      if (rpcMsg)
        deps.showWarning(`Cannot open local link: ${rpcMsg}`, probe.error)
      else
        deps.showWarning('Cannot open local link', probe.error)
      return
    }

    const matched = probe.match
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

    const resolved = await resolveTreeDirectoryPath(seq, source, logicalPath, canonicalPath, treeRoot)
    if (resolved.cancelled)
      return

    if (resolved.treePath !== undefined) {
      deps.revealDirectory({
        path: resolved.treePath,
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
