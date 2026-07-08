import { createEffect, createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createDialogState, createToggleDialog } from '~/hooks/createDialogState'
import { flush } from '~/test-support/async'

describe('createDialogState', () => {
  it('starts closed: value is null', () => {
    createRoot((dispose) => {
      const d = createDialogState<{ id: string }>()
      expect(d.value()).toBeNull()
      dispose()
    })
  })

  it('open(value) stores the payload; close() clears it', () => {
    createRoot((dispose) => {
      const d = createDialogState<{ id: string }>()
      d.open({ id: 'one' })
      expect(d.value()).toEqual({ id: 'one' })
      d.close()
      expect(d.value()).toBeNull()
      dispose()
    })
  })

  it('open() replaces the previous payload without going through null', () => {
    // Regression guard: AppShellDialogs reads value() inside <Show>; a
    // null intermediate would unmount the dialog and the user would see a
    // flicker.
    createRoot((dispose) => {
      const d = createDialogState<{ id: string }>()
      d.open({ id: 'one' })
      d.open({ id: 'two' })
      expect(d.value()).toEqual({ id: 'two' })
      dispose()
    })
  })

  it('open() with a function value stores the function literally (not invoked)', () => {
    // value() returns T | null where T is the payload — for payloads that
    // *are* functions (callbacks/resolvers), the setter must not unwrap
    // them. KeyPinConfirmState.resolve and WorkspaceConfirmPayload.resolve
    // are real-world payloads of this shape.
    createRoot((dispose) => {
      const d = createDialogState<{ resolve: () => string }>()
      const resolver = () => 'ok'
      d.open({ resolve: resolver })
      const stored = d.value()
      expect(stored?.resolve).toBe(resolver)
      expect(stored?.resolve()).toBe('ok')
      dispose()
    })
  })

  it('value() is reactive: createEffect re-runs when the payload changes', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const d = createDialogState<{ id: string }>()
        const seen: (string | null)[] = []
        createEffect(() => {
          seen.push(d.value()?.id ?? null)
        })
        await flush()
        d.open({ id: 'first' })
        await flush()
        d.open({ id: 'second' })
        await flush()
        d.close()
        await flush()
        expect(seen).toEqual([null, 'first', 'second', null])
        dispose()
        done()
      })
    })
  })

  it('close() on an already-closed dialog is a no-op', () => {
    createRoot((dispose) => {
      const d = createDialogState<{ id: string }>()
      d.close()
      expect(d.value()).toBeNull()
      d.close()
      expect(d.value()).toBeNull()
      dispose()
    })
  })

  describe('update', () => {
    // The update method merges a partial patch into the current payload
    // without closing/reopening the dialog. AppShellDialogs uses it for
    // the LastTabCloseDialog refresh path so a re-inspect doesn't flash
    // the dialog closed; preserving identity of non-patched fields also
    // protects callbacks (resolve, onDismiss) from being clobbered by
    // an accidental field-collision spread.

    it('returns false and does not write when the dialog is closed', () => {
      createRoot((dispose) => {
        const d = createDialogState<{ id: string, label: string }>()
        const wrote = d.update({ label: 'new' })
        expect(wrote).toBe(false)
        expect(d.value()).toBeNull()
        dispose()
      })
    })

    it('returns true and merges patch into the current payload', () => {
      createRoot((dispose) => {
        const d = createDialogState<{ id: string, label: string, count: number }>()
        d.open({ id: 'one', label: 'first', count: 1 })
        const wrote = d.update({ label: 'updated' })
        expect(wrote).toBe(true)
        expect(d.value()).toEqual({ id: 'one', label: 'updated', count: 1 })
        dispose()
      })
    })

    it('leaves untouched fields verbatim (including non-primitives by identity)', () => {
      // The LastTabCloseDialog state carries a `resolve` callback that
      // must survive a refresh. Pin the identity to catch any future
      // "spread + assign" implementation that accidentally drops fields.
      createRoot((dispose) => {
        const resolve = vi.fn()
        interface State { id: string, resolve: () => void, gitState: { dirty: boolean } | null }
        const d = createDialogState<State>()
        d.open({ id: 'one', resolve, gitState: null })
        d.update({ gitState: { dirty: true } })
        const after = d.value()
        expect(after?.id).toBe('one')
        expect(after?.resolve).toBe(resolve)
        expect(after?.gitState).toEqual({ dirty: true })
        dispose()
      })
    })

    it('subsequent updates compound (each merges on top of the previous result)', () => {
      createRoot((dispose) => {
        const d = createDialogState<{ a: number, b: number, c: number }>()
        d.open({ a: 1, b: 1, c: 1 })
        d.update({ a: 2 })
        d.update({ b: 3 })
        d.update({ c: 4, a: 5 })
        expect(d.value()).toEqual({ a: 5, b: 3, c: 4 })
        dispose()
      })
    })

    it('update fires reactivity exactly once per call', async () => {
      // Solid's createSignal default `equals: ===` would dedupe identical
      // values, but every update produces a fresh `{ ...current, ...patch }`
      // object, so the signal sees an identity change every call. Pin the
      // emission count so a future refactor (e.g. equality on shallow
      // shape) can't quietly mask a refresh.
      await new Promise<void>((done) => {
        createRoot(async (dispose) => {
          const d = createDialogState<{ a: number }>()
          d.open({ a: 1 })
          let runs = 0
          createEffect(() => {
            void d.value()
            runs++
          })
          await flush()
          expect(runs).toBe(1)
          d.update({ a: 2 })
          await flush()
          expect(runs).toBe(2)
          d.update({ a: 2 }) // same value, but fresh object identity
          await flush()
          expect(runs).toBe(3)
          dispose()
          done()
        })
      })
    })

    it('does not transition through null (the dialog stays mounted)', async () => {
      // Regression guard for the original "spread + reopen" pattern: a
      // close-then-reopen would briefly emit `null` and cause
      // <Show when={value()}> to unmount the dialog. update must not.
      await new Promise<void>((done) => {
        createRoot(async (dispose) => {
          const d = createDialogState<{ a: number }>()
          d.open({ a: 1 })
          const seen: (number | null)[] = []
          createEffect(() => {
            seen.push(d.value()?.a ?? null)
          })
          await flush()
          d.update({ a: 2 })
          await flush()
          d.update({ a: 3 })
          await flush()
          expect(seen).toEqual([1, 2, 3])
          // Importantly, null is never emitted — the dialog stays open.
          expect(seen).not.toContain(null)
          dispose()
          done()
        })
      })
    })

    it('an empty patch is a no-op-but-still-writes (caller-observable)', () => {
      // update({}) re-projects the payload into a fresh object. Document
      // that it returns true (the dialog is open) but the visible fields
      // are unchanged — useful so a "no-op refresh" call site doesn't
      // accidentally close the dialog.
      createRoot((dispose) => {
        const d = createDialogState<{ a: number }>()
        d.open({ a: 1 })
        const wrote = d.update({})
        expect(wrote).toBe(true)
        expect(d.value()).toEqual({ a: 1 })
        dispose()
      })
    })
  })
})

describe('createToggleDialog', () => {
  it('starts closed: isOpen is false', () => {
    createRoot((dispose) => {
      const d = createToggleDialog()
      expect(d.isOpen()).toBe(false)
      dispose()
    })
  })

  it('open()/close() flips isOpen', () => {
    createRoot((dispose) => {
      const d = createToggleDialog()
      d.open()
      expect(d.isOpen()).toBe(true)
      d.close()
      expect(d.isOpen()).toBe(false)
      dispose()
    })
  })

  it('open() is idempotent (no spurious flicker if called twice)', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const d = createToggleDialog()
        const observed: boolean[] = []
        createEffect(() => {
          observed.push(d.isOpen())
        })
        await flush()
        d.open()
        await flush()
        d.open()
        await flush()
        // Solid skips effect re-runs when the new signal value equals
        // the old via `===`. The second open() must not push another
        // `true` into observed.
        expect(observed).toEqual([false, true])
        dispose()
        done()
      })
    })
  })

  it('isOpen is reactive', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const d = createToggleDialog()
        const seen: boolean[] = []
        createEffect(() => {
          seen.push(d.isOpen())
        })
        await flush()
        d.open()
        await flush()
        d.close()
        await flush()
        expect(seen).toEqual([false, true, false])
        dispose()
        done()
      })
    })
  })

  it('the two handle types are interchangeable as function-shaped values', () => {
    // AppShell wires both ToggleDialogState and DialogState<T> into the
    // same `dialogs` record; consumers must be able to call .open() / .close()
    // without runtime errors regardless of which shape they received.
    const toggle = createToggleDialog()
    const stateful = createDialogState<{ id: string }>()
    const close = vi.fn()
    close.mockImplementation(toggle.close)
    close()
    close.mockImplementation(stateful.close)
    close()
    expect(close).toHaveBeenCalledTimes(2)
  })
})
