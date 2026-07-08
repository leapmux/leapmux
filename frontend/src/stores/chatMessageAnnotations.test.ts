import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createMessageAnnotationStore } from '~/stores/chatMessageAnnotations'

describe('chatMessageAnnotations', () => {
  it('starts empty and sets/clears a delivery error', () =>
    createRoot((dispose) => {
      const store = createMessageAnnotationStore()
      expect(store.errors).toEqual({})
      store.setError('m1', 'offline')
      expect(store.errors.m1).toBe('offline')
      store.clearError('m1')
      expect(store.errors.m1).toBeUndefined()
      dispose()
    }))

  it('tracks pending labels independently of errors', () =>
    createRoot((dispose) => {
      const store = createMessageAnnotationStore()
      store.setPendingLabel('m1', 'queued')
      store.setError('m1', 'oops')
      expect(store.pendingLabels.m1).toBe('queued')
      expect(store.errors.m1).toBe('oops')
      // Clearing one leaves the other untouched.
      store.clearPendingLabel('m1')
      expect(store.pendingLabels.m1).toBeUndefined()
      expect(store.errors.m1).toBe('oops')
      dispose()
    }))

  it('batch-clears only the errors that exist, leaving others intact', () =>
    createRoot((dispose) => {
      const store = createMessageAnnotationStore()
      store.setError('m1', 'a')
      store.setError('m2', 'b')
      store.setError('m3', 'c')
      // Mix present and absent ids: only present ones are deleted, the rest are
      // a guarded no-op, and an unlisted id (m3) survives.
      store.clearErrors(['m1', 'm2', 'absent'])
      expect(store.errors.m1).toBeUndefined()
      expect(store.errors.m2).toBeUndefined()
      expect(store.errors.m3).toBe('c')
      dispose()
    }))

  it('batch-clears pending labels independently of errors', () =>
    createRoot((dispose) => {
      const store = createMessageAnnotationStore()
      store.setPendingLabel('m1', 'queued')
      store.setPendingLabel('m2', 'starting')
      store.setError('m1', 'oops')
      store.clearPendingLabels(['m1', 'm2', 'absent'])
      expect(store.pendingLabels.m1).toBeUndefined()
      expect(store.pendingLabels.m2).toBeUndefined()
      // The error annotation for the same id is untouched by a pending-label clear.
      expect(store.errors.m1).toBe('oops')
      dispose()
    }))

  it('guarded batch clears short-circuit on absent / empty input without disturbing state', () =>
    createRoot((dispose) => {
      const store = createMessageAnnotationStore()
      store.setError('m1', 'a')
      // Clearing ids that carry no annotation, or an empty list, is a guarded
      // no-op (does not throw, leaves the present annotation intact).
      store.clearErrors(['x', 'y'])
      store.clearErrors([])
      store.clearPendingLabels(['x'])
      expect(store.errors.m1).toBe('a')
      dispose()
    }))
})
