/**
 * Lightweight container for an imperative callback shared across module
 * boundaries (e.g. parent components handing a "focus the editor" or
 * "scroll to bottom" thunk down to a child that registers the
 * implementation, then later invoking it from sibling code paths).
 *
 * Usage:
 *   const focusEditor = createImperativeRef<() => void>()
 *   focusEditor()?.()           // invoke (no-op if not registered)
 *   focusEditor.set(fn)         // register
 *   focusEditor.set(undefined)  // clear
 *
 * Prefer this over `{ current: T | undefined }` boxes — `set` is explicit,
 * and the callable form keeps the read site as terse as `xxx()?.()`.
 */
export interface ImperativeRef<T> {
  (): T | undefined
  set: (value: T | undefined) => void
}

export function createImperativeRef<T>(): ImperativeRef<T> {
  let value: T | undefined
  const ref = (() => value) as ImperativeRef<T>
  ref.set = (next) => {
    value = next
  }
  return ref
}
