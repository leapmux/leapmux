/// <reference types="vitest/globals" />
import type { Component, Context, ParentComponent, ParentProps } from 'solid-js'
import type { Registry } from 'solid-refresh'
import { render } from '@solidjs/testing-library'
import { createContext, useContext } from 'solid-js'
import { $$component, $$context, $$refresh, $$registry } from 'solid-refresh'
import { describe, expect, it } from 'vitest'
import { createStableContext } from './createStableContext'

/**
 * A stand-in for Vite's `import.meta.hot`, faithful to the parts solid-refresh
 * touches: `data` persists across re-evaluations of the module it belongs to,
 * `accept(cb)` records the patch callback, and `fire()` runs the recorded
 * callbacks once every module in an update batch has executed.
 *
 * One deliberate simplification: Vite runs exactly ONE generation's callbacks
 * (`HMRContext`'s constructor clears `mod.callbacks`, and `fetchUpdate`
 * snapshots them before re-importing), whereas `fire()` replays every
 * generation's. That is harmless here and kept for the simpler helper: each
 * `$$refreshESM` callback closes only over `hot` and re-reads
 * `hot.data['solid-refresh']` at call time, so a repeat is an idempotent
 * re-`patchRegistry`. A test that adds a THIRD generation, or a callback that
 * is not idempotent, would need this to drop the stale ones first.
 */
function makeHot() {
  const callbacks: ((module?: unknown) => void)[] = []
  return {
    // Vite hands over an empty bag; solid-refresh populates both keys on first use.
    data: {} as { 'solid-refresh': Registry, 'solid-refresh-prev': Registry },
    accept: (cb: (module?: unknown) => void) => {
      callbacks.push(cb)
    },
    invalidate: () => {},
    decline: () => {},
    fire: () => {
      for (const cb of callbacks) cb({})
    },
  }
}

type FakeHot = ReturnType<typeof makeHot>

/**
 * Evaluate one generation of a two-module pair under solid-refresh: module A
 * owns a context and its Provider, module B consumes it. Calling this twice
 * with the same `hot` objects is what an HMR batch does to both modules.
 */
function evaluateModules(makeCtx: () => Context<string | undefined>, hotA: FakeHot, hotB: FakeHot) {
  const regA = $$registry()
  const Ctx = $$context(regA, 'Ctx', makeCtx())
  const Provider: ParentComponent = $$component(regA, 'Provider', (props: ParentProps) => (
    <Ctx.Provider value="ok">{props.children}</Ctx.Provider>
  ))
  const useCtx = () => {
    const value = useContext(Ctx)
    if (!value)
      throw new Error('useCtx must be used within Provider')
    return value
  }
  $$refresh('vite', hotA, regA)

  // Module B is rewritten to import THIS generation of A, so it closes over
  // whichever context object A just handed out.
  const regB = $$registry()
  const Guard: Component = $$component(regB, 'Guard', () => <span>{useCtx()}</span>)
  $$refresh('vite', hotB, regB)

  return { Provider, Guard }
}

/**
 * Mount the pair, re-evaluate both modules as an HMR batch, then fire the patch
 * callbacks in `order`. Returns the thunk that applies the patches so callers
 * can assert on whether it throws.
 */
function hmrBatch(makeCtx: () => Context<string | undefined>, order: 'consumer-first' | 'context-first') {
  const hotA = makeHot()
  const hotB = makeHot()

  const first = evaluateModules(makeCtx, hotA, hotB)
  const { container } = render(() => (
    <first.Provider><first.Guard /></first.Provider>
  ))
  expect(container.textContent).toBe('ok')

  evaluateModules(makeCtx, hotA, hotB)

  return () => {
    for (const hot of order === 'consumer-first' ? [hotB, hotA] : [hotA, hotB])
      hot.fire()
  }
}

describe('createStableContext', () => {
  it('returns the same context for a repeated key', () => {
    const first = createStableContext<string>('test/repeat')
    const second = createStableContext<string>('test/repeat')
    expect(second).toBe(first)
  })

  it('returns distinct contexts for distinct keys', () => {
    expect(createStableContext<string>('test/a')).not.toBe(createStableContext<string>('test/b'))
  })

  it('serves the default value when no Provider is above the consumer', () => {
    const ctx = createStableContext<string>('test/default', 'fallback')
    const { container } = render(() => <span>{useContext(ctx)}</span>)
    expect(container.textContent).toBe('fallback')
  })

  it('adopts the newest default value on re-evaluation', () => {
    const first = createStableContext<string>('test/redefault', 'stale')
    const second = createStableContext<string>('test/redefault', 'fresh')
    expect(second).toBe(first)

    const { container } = render(() => <span>{useContext(first)}</span>)
    expect(container.textContent).toBe('fresh')
  })

  // A falsy default is still a default. Anything that reached for `||` rather
  // than an explicit undefined check -- here, or in the cache hit that
  // re-assigns it -- would silently swap `0` for the no-default behaviour.
  //
  // The two defaults must DIFFER, and the falsy one must come second: passing
  // `0` both times makes `defaultValue || cached.defaultValue` compute `0 || 0`
  // and the assertion holds against the very bug it is written to catch.
  it('serves a falsy default value', () => {
    const first = createStableContext<number>('test/falsy', 5)
    const second = createStableContext<number>('test/falsy', 0)
    expect(second).toBe(first)

    const { container } = render(() => <span>{String(useContext(second))}</span>)
    expect(container.textContent).toBe('0')
  })

  it('lets a Provider from one call serve a consumer holding another call', () => {
    const provided = createStableContext<string>('test/crossCall')
    const consumed = createStableContext<string>('test/crossCall')

    const { container } = render(() => (
      <provided.Provider value="shared">
        <span>{useContext(consumed) ?? 'missing'}</span>
      </provided.Provider>
    ))
    expect(container.textContent).toBe('shared')
  })

  it('does not share state between two plain createContext calls', () => {
    // The contrast that makes the case above meaningful: this is precisely what
    // a module re-evaluation does without the pinning.
    const provided = createContext<string>()
    const consumed = createContext<string>()

    const { container } = render(() => (
      <provided.Provider value="shared">
        <span>{useContext(consumed) ?? 'missing'}</span>
      </provided.Provider>
    ))
    expect(container.textContent).toBe('missing')
  })

  describe('under an hmr batch touching a context module and its consumer', () => {
    it('reproduces the failure with plain createContext when the consumer patches first', () => {
      const apply = hmrBatch(() => createContext<string>(), 'consumer-first')
      expect(apply).toThrow('useCtx must be used within Provider')
    })

    it('survives with plain createContext when the context module patches first', () => {
      // Only the ordering differs, which is what makes the failure intermittent.
      const apply = hmrBatch(() => createContext<string>(), 'context-first')
      expect(apply).not.toThrow()
    })

    it('survives either ordering with createStableContext', () => {
      const consumerFirst = hmrBatch(() => createStableContext<string>('test/hmrConsumerFirst'), 'consumer-first')
      expect(consumerFirst).not.toThrow()

      const contextFirst = hmrBatch(() => createStableContext<string>('test/hmrContextFirst'), 'context-first')
      expect(contextFirst).not.toThrow()
    })
  })
})
