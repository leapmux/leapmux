import type { Accessor } from 'solid-js'
import type { TreeNodeData } from './directoryTreeState'
import type { DirectoryListing } from '~/generated/proto/leapmux/v1/file_pb'
import type { PathFlavor } from '~/lib/paths'
import { createEffect, createSignal, on } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { formatErrorMessage } from '~/lib/errors'
import { join, normalizeSeparators, relativeUnder, split } from '~/lib/paths'
import { samePath } from './directoryTreeState'

// -------------------------------------------------------------------------
// File listing
// -------------------------------------------------------------------------

/** One directory's listing, as the tree caches it. */
export interface DirectoryListingData {
  path: string
  entries: TreeNodeData[]
  truncated: boolean
  totalEntries: number
}

function toListingData(listing: DirectoryListing): DirectoryListingData {
  return {
    path: listing.path,
    entries: listing.entries.map(entry => ({
      path: entry.path,
      displayName: entry.name,
      isDir: entry.isDir,
      hidden: entry.hidden,
      // `size` is a protobuf int64, so the wire type is bigint. File sizes stay
      // far below Number.MAX_SAFE_INTEGER, and JSON.stringify -- which the
      // sessionStorage cache runs on this value -- throws on a bigint.
      size: Number(entry.size ?? 0n),
      modTime: entry.modTime,
    })),
    truncated: listing.truncated,
    totalEntries: listing.totalEntries,
  }
}

/**
 * One ListDirectory answer: the listings, and the directory a READ failure
 * stopped the chain at.
 *
 * `unreadable` is absent when the chain is complete AND when a cap cut it: a
 * listing count and a payload budget are not failures, and the caller lists
 * the rest itself. So a reason here always has a row to sit on.
 */
interface ChainResult {
  listings: DirectoryListingData[]
  unreadable?: { path: string, reason: string }
}

/**
 * List one directory, or -- when `fromRoot` is given -- every directory from
 * `fromRoot` down to `dirPath`, outermost first.
 *
 * The chain form is what lets a tree rooted at the FILESYSTEM ROOT reveal a
 * deep selection without one round trip per level. Rooted at `/` and revealing
 * `/home/alice/proj`, the per-node cascade issued four sequential requests,
 * because each level can only ask once the previous level's listing mounted
 * it. One request now answers all four.
 *
 * No sort here: the cache holds the worker's order (directories first, then
 * name), and the display order is applied when the rows render, so changing the
 * sort reorders what is already on screen instead of re-fetching every
 * expanded directory.
 */
export async function loadListings(
  workerId: string,
  dirPath: string,
  showFiles: boolean,
  fromRoot?: string,
): Promise<ChainResult> {
  const resp = await workerRpc.listDirectory(workerId, {
    workerId,
    path: dirPath,
    maxDepth: 5,
    dirsOnly: !showFiles,
    ...(fromRoot ? { fromRoot } : {}),
  })
  return {
    listings: resp.listings.map(toListingData),
    unreadable: resp.unreadable && { path: resp.unreadable.path, reason: resp.unreadable.reason },
  }
}

/** The single-directory form every per-node caller uses. */
export async function loadChildren(
  workerId: string,
  dirPath: string,
  showFiles: boolean,
): Promise<DirectoryListingData> {
  const [only] = (await loadListings(workerId, dirPath, showFiles)).listings
  // A worker that answers a single-directory request with NO listing committed
  // a protocol violation, not "the directory is empty" -- an empty directory
  // answers with one listing whose `entries` is empty. Throw rather than cache
  // a phantom listing, which the "already loaded" guard would then honour for
  // the rest of the session.
  if (!only)
    throw new Error(`ListDirectory returned no listing for ${dirPath}`)
  return only
}

/**
 * Every directory from `root` down to `target`, outermost first, or `[root]`
 * when the target is not under the root.
 *
 * `split` rather than a scan for separators, because on win32 the VOLUME is one
 * leading segment: a hand-rolled walk over the separator would emit `C:` as an
 * ancestor, and `C:` identifies the current directory on drive C, not its
 * root.
 *
 * Both inputs are normalized first, for the reason `isDescendantPath` states:
 * a target the user typed as `C:/Users/alice` is under a root spelled `C:\`,
 * and an un-normalized comparison says it is not.
 */
export function ancestorChain(root: string, target: string, flavor: PathFlavor): string[] {
  const rel = target ? relativeUnder(normalizeSeparators(target, flavor), normalizeSeparators(root, flavor), flavor) : null
  if (!rel)
    return [root]
  const chain = [root]
  let cur = root
  for (const part of split(rel, flavor)) {
    cur = join([cur, part], flavor)
    chain.push(cur)
  }
  return chain
}

/** What {@link createDirectoryListings} reads from the tree, and writes back. */
export interface DirectoryListingsOptions {
  workerId: Accessor<string>
  rootPath: Accessor<string>
  showFiles: Accessor<boolean>
  flavor: Accessor<PathFlavor>
  /** False suspends the loader entirely: a hidden tree asks for nothing. */
  enabled: Accessor<boolean>
  /**
   * The key of the cache this loader speaks for.
   *
   * When it changes, the caller replaces the whole store, so every claim and
   * the chain guard go with it: both describe the cache that just went away,
   * and neither can be rebuilt from the new one. The loader watches this
   * itself, rather than taking a `reset()` the caller must remember to call,
   * because the ORDER matters -- the drop has to happen before the chain
   * effect reads the new cache.
   */
  cacheKey: Accessor<string>
  /** The path the tree walks itself open toward. Drives the chain request. */
  revealTarget: Accessor<string>
  getChildren: (path: string) => TreeNodeData[] | undefined
  /** Write one directory's children. */
  setChildren: (path: string, entries: TreeNodeData[], truncated: boolean, totalEntries: number) => void
  /** Write a whole chain in ONE reactive pass. */
  setListings: (listings: readonly DirectoryListingData[]) => void
  /** Record, or clear, why a directory would not list. */
  setUnreadable: (path: string, reason: string | undefined) => void
}

export interface DirectoryListings {
  /** True only for the FIRST load, when the tree has nothing to show. */
  loading: Accessor<boolean>
  /** Set only when that first load failed, for the same reason. */
  error: Accessor<string | null>
  ensureChildren: (path: string) => Promise<void>
  refetchChildren: (path: string) => Promise<void>
}

/**
 * The tree's listing loader: what it asks the worker for, and when.
 *
 * Split from the component because none of it renders. It owns the in-flight
 * registry, the chain request and the guard that keeps that request from
 * re-firing on its own write; the component owns the store it writes into and
 * every row it draws.
 *
 * `loading` and `error` come back OUT because the tree renders them, and
 * because only the loader knows which load is the first one -- the one with
 * nothing on screen behind it, and therefore the only one whose failure is
 * worth a full-pane error.
 */
export function createDirectoryListings(opts: DirectoryListingsOptions): DirectoryListings {
  const [loading, setLoading] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  let loadVersion = 0

  /**
   * Directories with a listing request already in flight, and the request that
   * will supply each of them.
   *
   * A plain Map, NOT reactive: it records the requests in flight right now,
   * and a node that re-rendered because of it would learn nothing it does not
   * already learn from the cache. The chain loader claims every directory it is about
   * to fetch BEFORE it awaits, so a per-node load that starts in the same tick
   * finds the claim and awaits it.
   */
  const inFlight = new Map<string, Promise<void>>()

  /** Register one promise as the pending listing for each of `paths`. */
  const claimInFlight = (paths: readonly string[], work: Promise<void>) => {
    const settled = work.finally(() => {
      for (const path of paths) {
        if (inFlight.get(path) === settled)
          inFlight.delete(path)
      }
    })
    for (const path of paths)
      inFlight.set(path, settled)
    return settled
  }

  /**
   * Drop every claim, because the answers they promise no longer belong to
   * this tree.
   *
   * The store is replaced wholesale when the worker or the root changes, and
   * `inFlight` must go with it. Two workers routinely share a path -- every
   * POSIX worker roots the picker at `/` and reveals `/home` -- so a claim
   * left behind is one a node of the NEXT worker awaits, and it resolves with
   * the previous worker's listing.
   */
  const clearInFlight = () => inFlight.clear()

  /**
   * The last chain request this tree issued, as workerId + root + target.
   *
   * NOT reactive, and deliberately not derived from the cache. The worker
   * CANONICALIZES what it lists, so a symlinked ancestor comes back under a
   * path the chain never listed -- the chain effect's cache guard then misses
   * forever, and that effect reads `childrenCache`, so it would re-fetch every
   * time its own write landed. This key makes that loop impossible rather than
   * leaving it to the unchanged-content fast path to damp.
   *
   * It describes the request whose result the CURRENT cache holds, so the
   * effect below clears it whenever it replaces that cache.
   */
  let lastChainKey = ''

  /**
   * List one directory and cache it, whatever is already in flight for it.
   *
   * The worker is CAPTURED, not read again in the callback: the props can move
   * to another worker while the request is open, and two workers routinely
   * share a path -- every POSIX worker roots the picker at `/`. Without the
   * guard the answer lands in the next worker's tree, which then shows one
   * machine's directories labelled as another's.
   */
  const refetchChildren = (path: string): Promise<void> => {
    const workerId = opts.workerId()
    const showFiles = opts.showFiles()
    const work = loadChildren(workerId, path, showFiles)

      .then((result) => {
        if (workerId !== opts.workerId())
          return
        opts.setChildren(path, result.entries, result.truncated, result.totalEntries)
      })

      .catch((err) => {
        // A failed listing leaves the node collapsed, and now says why. This
        // is the only surface for it: the tree's one error slot belongs to the
        // FIRST load, so a per-node failure used to disappear entirely.
        if (workerId === opts.workerId())
          opts.setUnreadable(path, formatErrorMessage(err, 'cannot be read'))
      })
    return claimInFlight([path], work)
  }

  const ensureChildren = async (path: string): Promise<void> => {
    const pending = inFlight.get(path)
    if (pending) {
      await pending
      // A claim is a HINT, not a guarantee. The chain loader claims every
      // directory it asks about, and the worker may answer with fewer
      // listings -- it caps the chain by count and by payload budget, and it
      // stops at a level it cannot read. Re-check, and ask for what the
      // response did not carry.
      if (opts.getChildren(path) !== undefined)
        return
      // Through `ensureChildren` again, not `refetchChildren`: two nodes that
      // awaited the same claim must not each start a request. The claim above
      // is already deleted by the time any awaiter resumes, so this recurses
      // exactly once.
      return ensureChildren(path)
    }
    return refetchChildren(path)
  }

  // BEFORE the chain effect below, so on a cacheKey change this runs first and
  // that effect reads a loader with nothing stale in it. Solid runs effects in
  // creation order, so the position of this call is the guarantee.
  createEffect(on(opts.cacheKey, () => {
    clearInFlight()
    lastChainKey = ''
  }))

  /**
   * The chain, minus a target the tree already knows is a FILE.
   *
   * A file has no listing, so keeping it would leave the "everything cached"
   * guard permanently false and fire a request on every click of that file.
   * Before its parent's listing arrives the tree knows nothing about it, so
   * the file survives exactly one request and drops out afterwards. The worker
   * ends the chain at the parent as well, so this guard is a second, redundant
   * one -- but it is the one that removes the REQUEST, which the worker's own
   * rule cannot do.
   */
  const revealChain = (root: string, target: string, flavor: PathFlavor): string[] => {
    const chain = ancestorChain(root, target, flavor)
    if (chain.length < 2)
      return chain
    const parent = chain[chain.length - 2]
    const known = opts.getChildren(parent)?.find(c => samePath(c.path, target, flavor))
    return known && !known.isDir ? chain.slice(0, -1) : chain
  }

  // Load the root listing and, when the tree has somewhere to reveal, every
  // listing between the root and it -- in ONE request.
  //
  // This is what makes a root of `/` affordable. Revealing `/home/alice/proj`
  // through the per-node cascade alone costs four SEQUENTIAL requests, because
  // each level can only ask once the previous level's listing mounted it.
  createEffect(() => {
    const workerId = opts.workerId()
    const root = opts.rootPath()
    const target = opts.revealTarget()
    if (!workerId)
      return
    if (!opts.enabled())
      return

    const chain = revealChain(root, target, opts.flavor())
    // Skip only when EVERY directory on the chain is cached (from
    // sessionStorage or a previous load), which also removes the flicker on a
    // tab switch. The old guard tested the root alone, which sufficed while
    // the root was all this effect fetched; a cached root beside an uncached
    // ancestor is now the normal state right after a path is typed.
    if (chain.every(p => opts.getChildren(p) !== undefined))
      return

    const chainKey = JSON.stringify([workerId, root, target])
    if (chainKey === lastChainKey)
      return
    lastChainKey = chainKey

    // The loading and error UI belongs to the FIRST load, when the tree has
    // nothing to show. A reveal that runs with the root already on screen is
    // silent -- the same rule the refresh path below states -- so changing the
    // selection never blanks the tree, and a failed chain never paints an
    // error over a working one. The per-node cascade still walks the user
    // there one level at a time when this request fails.
    const initialRootCached = opts.getChildren(root) !== undefined
    const chained = chain.length > 1
    const version = ++loadVersion
    if (initialRootCached) {
      // This run supersedes any pending one, and it has content on screen, so
      // it will never lower `loading` itself. Lower it here instead: without
      // this, a run that started with the root uncached raises `loading`, this
      // run invalidates it by `loadVersion`, and nobody ever lowers it -- the
      // whole tree stays behind "Loading..." for the life of the component.
      setLoading(false)
    }
    else {
      setLoading(true)
      setError(null)
    }
    // The chain's OWN deepest element, not the raw target. `revealChain` drops
    // a target it already knows is a file, and sending the file anyway makes
    // the worker repeat that rule -- two implementations of one decision.
    const deepest = chain[chain.length - 1]
    const work = loadListings(workerId, deepest, opts.showFiles(), chained ? root : undefined)

      .then((resp) => {
        if (version !== loadVersion)
          return
        const { listings } = resp
        if (listings.length === 0)
          throw new Error(`ListDirectory returned no listings for ${root}`)
        // Re-key the FIRST listing to the root we asked for. The worker
        // canonicalizes what it lists, so a root that is a symlink answers
        // under a different path -- `/tmp/ws` as `/private/tmp/ws` on macOS --
        // and the root row's cache is keyed by `opts.rootPath()`. Deeper
        // listings keep the worker's spelling, because that is what the
        // entries' own paths, and therefore the tree's nodes, are built from.
        //
        // This repairs the ROOT key alone, and it cannot do more. When the
        // worker canonicalizes a level BELOW the root, the level count changes
        // with it -- `/tmp/ws` is two levels under `/` and `/private/tmp/ws`
        // is three -- so no pairing by index recovers the caller's spelling.
        // Those middle listings then land under keys no node reads, the
        // "everything cached" guard above stays false, and `lastChainKey`
        // is what stops the effect re-firing on its own write. The tree still
        // reaches the target: the per-node cascade re-lists each such level
        // under the path it asked for, at one round trip per level. The chain
        // is an optimization, and this is the case where it does not apply.
        opts.setListings([{ ...listings[0], path: root }, ...listings.slice(1)])
        // The worker names the directory its chain stopped at, and why. Set it
        // AFTER the listings, because writing a listing clears the reason for
        // that path and the unreadable one carries no listing.
        if (resp.unreadable)
          opts.setUnreadable(resp.unreadable.path, resp.unreadable.reason)
        if (!initialRootCached)
          setLoading(false)
      })
      .catch((err) => {
        if (version !== loadVersion)
          return
        // Let the next run retry: the failure was this request's, not this
        // (workerId, root, target)'s.
        lastChainKey = ''
        if (!initialRootCached) {
          setError(formatErrorMessage(err, 'Failed to load directory'))
          setLoading(false)
        }
      })
    // Claimed AFTER the promise exists but BEFORE anything awaits it, so a
    // node mounted by this same reveal finds the claim rather than issuing its
    // own request for a directory this response already carries.
    claimInFlight(chain, work)
  })

  return { loading, error, ensureChildren, refetchChildren }
}
