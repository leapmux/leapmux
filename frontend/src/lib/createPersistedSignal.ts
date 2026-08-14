import type { Accessor, Setter } from 'solid-js'
import { createEffect, createSignal, on } from 'solid-js'
import { localStorageGet, localStorageSet } from '~/lib/browserStorage'

/**
 * A signal backed by localStorage under a key that may itself change.
 *
 * Two rules make a per-scope preference behave, and both are easy to write
 * once and forget the next time:
 *
 *  - **Re-read when the KEY changes.** The Files section keys its preferences
 *    by `(workerId, workingDir)`, so switching tabs must load that scope's
 *    stored value rather than carry the previous tab's over.
 *  - **Never write on mount.** The persist effect is deferred, so simply
 *    reading a preference does not rewrite it -- which would refresh its TTL
 *    and resurrect a key the storage sweep was about to drop.
 *
 * `parse` validates whatever came back. The stored value is arbitrary JSON
 * that an older build or a hand edit may have written, so it is `unknown`, and
 * `parse` also supplies the default for an absent or unusable value.
 * `parseFileSortOrder` in `~/lib/fileSort` is written to this shape.
 *
 * A constant `key` is fine: the re-read effect is deferred, so it never fires
 * for a key that cannot change.
 *
 * Lives beside `~/lib/persistedSeq` rather than inside `~/lib/browserStorage`,
 * which is a framework-free key registry and imports nothing from `solid-js`.
 */
export function createPersistedSignal<T>(
  key: Accessor<string>,
  parse: (stored: unknown) => T,
): [Accessor<T>, Setter<T>] {
  const [value, setValue] = createSignal<T>(parse(localStorageGet(key())))

  createEffect(on(key, (k) => {
    setValue(() => parse(localStorageGet(k)))
  }, { defer: true }))

  createEffect(on(value, (v) => {
    localStorageSet(key(), v)
  }, { defer: true }))

  return [value, setValue]
}

/** A `parse` for a stored boolean, with an explicit default. */
export function persistedBoolean(fallback: boolean): (stored: unknown) => boolean {
  return stored => (typeof stored === 'boolean' ? stored : fallback)
}
