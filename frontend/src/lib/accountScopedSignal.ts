import { createEffect, createRoot, createSignal } from 'solid-js'
import { hasStorageAccount, onStorageAccountChange } from '~/lib/browserStorage'

/**
 * How a value survives a reload, for {@link createAccountScopedSignal}.
 *
 * Omit it for state that must NOT outlive the page: the signal then keeps the
 * account scoping and the reset, and writes nothing.
 */
export interface AccountScopedPersistence<T> {
  /** Read the stored value for the CURRENT account. */
  read: () => T
  /** Write `value` under the current account. */
  write: (value: T) => void
}

export interface AccountScopedSignal<T> {
  /** The current value. Reactive. Seeds from storage on the first access. */
  get: () => T
  set: (next: T | ((prev: T) => T)) => void
  /** Drop the mirror and re-read. FOR TESTS ONLY. */
  reset: () => void
}

/**
 * A module-scope signal that belongs to the signed-in account.
 *
 * Several sidebar modules need the same four things, and each hand-rolled copy
 * was a place to get one of them wrong:
 *
 *  - ONE signal for the whole app, because the value is one document under one
 *    key and several components read it at once. A per-component signal wrote
 *    the whole value back with no merge and erased what the others held.
 *  - A LAZY first read. The module is imported when the bundle loads, and
 *    `AuthContext` calls `setStorageAccount` only once the identity resolves;
 *    an account-scoped read before that throws.
 *  - A persist effect that stays quiet until that first read, so it cannot
 *    overwrite a stored value the module has not seen.
 *  - A re-read when the account namespace moves, because the mirror belongs to
 *    the account it was read for.
 *
 * `createRoot`, because module scope has no owner: the effect would otherwise
 * be created outside any reactive root, never dispose, and log Solid's
 * "computations created outside a `createRoot`" warning. The root is never
 * disposed on purpose -- it lives as long as the module does.
 */
export function createAccountScopedSignal<T>(
  defaultValue: T,
  persist?: AccountScopedPersistence<T>,
): AccountScopedSignal<T> {
  /**
   * Whether storage was read for the CURRENT account yet.
   *
   * Declared ahead of the `createRoot` below: Solid flushes the root's effects
   * at the end of that call, so the persist effect reads this binding while
   * this function is still running.
   */
  let seeded = false

  const [value, setValue] = createRoot(() => {
    const [read, write] = createSignal<T>(defaultValue)
    if (persist) {
      createEffect(() => {
        const current = read()
        // Skip until the first read landed. Before an account is set a write
        // throws, and writing the placeholder would erase the stored value.
        if (!seeded)
          return
        persist.write(current)
      })
    }
    return [read, write] as const
  })

  function ensureSeeded(): void {
    if (seeded || !hasStorageAccount())
      return
    seeded = true
    if (persist)
      setValue(() => persist.read())
  }

  // The namespace moved, so the mirror belongs to the account that left.
  //
  // It re-seeds EAGERLY rather than waiting for the next access. Writing the
  // default alone is a no-op whenever that default is a module-level constant:
  // Solid's default equality is `===`, so the write notifies nobody and every
  // computation already subscribed keeps the previous account's value. Reading
  // the incoming account here notifies whenever the two differ.
  //
  // `setStorageAccount` assigns the account before it notifies, so
  // `hasStorageAccount()` is already true inside this callback.
  const onAccountMoved = () => {
    seeded = false
    setValue(() => defaultValue)
    ensureSeeded()
  }

  // Through a NAMED function, so `reset` can register it again.
  onStorageAccountChange(onAccountMoved)

  return {
    get: () => {
      ensureSeeded()
      return value()
    },
    set: (next) => {
      ensureSeeded()
      setValue(typeof next === 'function' ? (next as (prev: T) => T) : () => next)
    },
    reset: () => {
      seeded = false
      setValue(() => defaultValue)
      // `resetStorageAccountForTests` CLEARS every account listener, and the
      // suite runs it before each test -- so a module that subscribed at import
      // time is unsubscribed for the whole file, and its account-switch
      // behaviour disappears from every test with no failure to show for it.
      // Registering again here is what makes that behaviour reachable at all.
      // The listeners live in a Set keyed by reference, so this is a no-op in
      // production, where nothing calls `reset`.
      onStorageAccountChange(onAccountMoved)
    },
  }
}
