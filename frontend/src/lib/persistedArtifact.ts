import { getArtifact, isArtifactStoreAvailable, putArtifact, RENDER_ARTIFACT_CACHE_VERSION } from './renderArtifactStore'
import { syntaxThemeKey } from './shikiThemes'

/**
 * A reload warm-start cache for output that carries BAKED syntax colours.
 *
 * Two of these exist -- the tokenized code rows and the rendered markdown
 * bodies -- and they were written out twice: two namespace builders, two
 * availability-and-size guards, two readers that validate a stored shape before
 * returning it, and two writers. A third consumer would have copied it again.
 *
 * The one rule this exists to make structural is the one both copies had to
 * restate, and that this review had to repair in both: READ THE NAMESPACE AT
 * DISPATCH, AND PASS IT TO THE WRITE. `write` therefore takes an `ns` rather
 * than calling `ns()` itself. A writer runs from a worker reply, by which time
 * the user may have chosen another theme -- and reading the live pair there
 * filed old-theme output under the new theme's namespace, where the next
 * session read it back as valid and painted the abandoned theme's colours for
 * as long as the artifact lived.
 *
 * `read` uses the LIVE namespace, which is correct and is the asymmetry: a read
 * wants the artifacts of the theme showing now. It also RE-CHECKS that
 * namespace when the lookup resolves, and answers a miss when it moved. The
 * store round trip is asynchronous, so the user can choose another theme inside
 * it, and the value that comes back then belongs to a theme nobody is showing.
 * Every consumer feeds that value to a cache keyed on the source alone, so the
 * check has to happen once here rather than as a rule each caller restates --
 * one of the two callers had already forgotten it.
 */
export interface PersistedArtifact<S, R> {
  /** The namespace for the pair that is live NOW. Capture at dispatch. */
  ns: () => string
  /**
   * Look up an artifact. Returns undefined SYNCHRONOUSLY when the store cannot
   * serve here (no indexedDB, oversized source), so a caller keeps its
   * same-frame dispatch timing rather than paying an async hop for nothing.
   *
   * It answers a MISS when the namespace moved while the lookup was in flight,
   * so a caller may treat whatever it resolves to as belonging to the theme
   * that is showing now.
   */
  read: (source: string) => Promise<R | undefined> | undefined
  /** Store an artifact under the namespace its value was produced for. */
  write: (ns: string, source: string, value: S) => void
}

export interface PersistedArtifactOptions<S, R> {
  /** Short namespace prefix, e.g. `tok` or `md`. */
  prefix: string
  /** One pathological body must not dominate the store: the key embeds the source. */
  maxSourceLength: number
  /** Narrow whatever the store returned; anything else is treated as a miss. */
  isValid: (stored: unknown) => stored is S
  /** Map a valid stored value to what the caller wants, or undefined to miss. */
  decode: (stored: S) => R | undefined
}

export function createPersistedArtifact<S, R>(
  opts: PersistedArtifactOptions<S, R>,
): PersistedArtifact<S, R> {
  // The namespace folds in the cache version AND the theme names: a persisted
  // artifact outlives the bundle that produced it, so a wire-shape or
  // theme-contract change must ORPHAN the old entries rather than serve them.
  // Orphaned entries are never looked up again and the store's TTL sweep
  // collects them, so nothing has to delete them.
  const ns = (): string => `${opts.prefix}@${RENDER_ARTIFACT_CACHE_VERSION}|${syntaxThemeKey()}`

  return {
    ns,
    read: (source) => {
      if (!isArtifactStoreAvailable() || source.length > opts.maxSourceLength)
        return undefined
      // The namespace this lookup asks under, held so the answer can be tested
      // against the one that is live when it arrives.
      const asked = ns()
      return getArtifact<unknown>(asked, source).then((stored) => {
        // The theme moved during the round trip. This value carries the
        // abandoned theme's baked colours, so it is a miss: the caller
        // re-derives under the pair that is showing now.
        if (ns() !== asked)
          return undefined
        if (!opts.isValid(stored))
          return undefined
        return opts.decode(stored)
      })
    },
    write: (namespace, source, value) => {
      if (!isArtifactStoreAvailable() || source.length > opts.maxSourceLength)
        return
      void putArtifact(namespace, source, value)
    },
  }
}
