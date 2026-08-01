import type { Context } from 'solid-js'
import { createContext } from 'solid-js'

/**
 * The process-wide context cache.
 *
 * `Symbol.for` rather than a module-level `const`: the whole point is to
 * outlive a module re-evaluation, and this module is no more immune to being
 * re-executed than the ones calling it. A registry held in module scope would
 * reset exactly when it is needed most.
 */
const CONTEXTS = Symbol.for('leapmux.stableContexts')

interface ContextHost {
  [CONTEXTS]?: Map<string, Context<unknown>>
}

/**
 * `createContext` whose identity survives re-evaluation of the calling module.
 *
 * Solid keys every `useContext` lookup by an id minted inside `createContext`,
 * so a module that runs twice hands out two unrelated contexts -- a Provider
 * from the first writes an id the second's consumers never read. Vite's HMR
 * does precisely that: an update batch re-executes each changed module, and
 * solid-refresh repairs the split afterwards by rewriting the new context's id
 * back to the original. That repair is ordering-dependent. Accept callbacks
 * fire in the update payload's order, so an edit touching a context module and
 * one of its consumers together can re-render the consumer first, against an
 * id no mounted Provider ever wrote. `useContext` then returns undefined and
 * the consumer's guard throws -- "useAuth must be used within AuthProvider",
 * with nobody having touched the app.
 *
 * Pinning the object under a stable key removes the ordering dependence
 * entirely: every re-evaluation returns the first context, and solid-refresh's
 * repair degrades to a self-assignment. Production evaluates each module once,
 * so the cache is a single miss there; the path is shared rather than
 * dev-only so there is one behaviour to reason about.
 *
 * `key` must be unique app-wide -- two modules sharing one silently share a
 * context. Name it after the owning module's path (`'context/AuthContext'`),
 * which is unique by construction. A module owning several contexts
 * distinguishes them with a suffix (`'workspace/WorkspaceTabTree#rowSelection'`).
 */
export function createStableContext<T>(key: string): Context<T | undefined>
export function createStableContext<T>(key: string, defaultValue: T): Context<T>
export function createStableContext<T>(key: string, defaultValue?: T): Context<T | undefined> {
  const host = globalThis as ContextHost
  const cache = (host[CONTEXTS] ??= new Map<string, Context<unknown>>())

  const cached = cache.get(key) as Context<T | undefined> | undefined
  if (cached) {
    // Adopt the newest default rather than freezing whichever one the first
    // evaluation happened to pass. Rewriting `defaultValue` is the one thing
    // solid-refresh's context patch did that pinning does not make redundant,
    // and dropping it would quietly require a reload to pick up an edit to it.
    cached.defaultValue = defaultValue
    return cached
  }

  const created = createContext<T | undefined>(defaultValue)
  cache.set(key, created as Context<unknown>)
  return created
}
