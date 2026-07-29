import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import { describe, expect, it } from 'vitest'
import { isWorkerKnownOffline, isWorkerKnownOnline, onlineWorkerIdSet, workerOnlineState } from '~/lib/workerLiveness'

function worker(id: string, online: boolean): Worker {
  return { id, online } as Worker
}

describe('workerliveness', () => {
  it('reports a listed online worker as online', () => {
    expect(isWorkerKnownOnline([worker('w1', true)], 'w1')).toBe(true)
  })

  it('reports a listed offline worker as offline', () => {
    expect(isWorkerKnownOnline([worker('w1', false)], 'w1')).toBe(false)
  })

  it('answers per worker rather than for the list as a whole', () => {
    const workers = [worker('w1', true), worker('w2', false)]
    expect(isWorkerKnownOnline(workers, 'w1')).toBe(true)
    expect(isWorkerKnownOnline(workers, 'w2')).toBe(false)
  })

  /**
   * The fail-open half, and the reason this is a named function rather than an
   * inline `.some(...)`.
   *
   * The list is empty until `listWorkers` lands, so on first paint every id is
   * absent. Treating absence as offline would grey out the branch menu on every
   * page load until the fetch returned — a visible flicker on working actions,
   * which is worse than letting an action fail with an error the user can act
   * on. Only a positive offline reading gates anything.
   */
  it('treats an unlisted worker as online', () => {
    expect(isWorkerKnownOnline([], 'w1')).toBe(true)
    expect(isWorkerKnownOnline([worker('w2', false)], 'w1')).toBe(true)
  })

  /**
   * The raw reading keeps "unknown" distinct, which is the whole reason it is
   * separate from the fail-open policy above. A destructive caller -- the
   * last-tab close, which skips the uncommitted-work dialog on a `false` --
   * must be able to tell "the list says this machine is down" from "the list
   * has not loaded". Collapsing the two is how a healthy Worker's tab gets
   * retired without the prompt.
   */
  it('distinguishes unknown from offline', () => {
    const workers = [worker('w1', true), worker('w2', false)]
    expect(workerOnlineState(workers, 'w1')).toBe(true)
    expect(workerOnlineState(workers, 'w2')).toBe(false)
    expect(workerOnlineState(workers, 'w3')).toBeUndefined()
    expect(workerOnlineState([], 'w1')).toBeUndefined()
  })
})

describe('isWorkerKnownOffline', () => {
  // The mirror of isWorkerKnownOnline, and deliberately NOT its negation: both
  // treat "not in the list" as reachable, because that state means "the list has
  // not arrived" at least as often as "the machine is gone". Only a positive
  // offline report may unlock a path that retires a tab.
  it('is true only for a worker the list positively reports offline', () => {
    const workers = [worker('w-on', true), worker('w-off', false)]
    expect(isWorkerKnownOffline(workers, 'w-off')).toBe(true)
    expect(isWorkerKnownOffline(workers, 'w-on')).toBe(false)
    expect(isWorkerKnownOffline(workers, 'w-absent')).toBe(false)
    expect(isWorkerKnownOffline([], 'w-off')).toBe(false)
  })

  it('is not the negation of isWorkerKnownOnline for an unknown id', () => {
    // Both answer "reachable" for an id the list has not mentioned. A caller that
    // assumed they partition the space would read first paint as all-offline.
    expect(isWorkerKnownOnline([], 'w-absent')).toBe(true)
    expect(isWorkerKnownOffline([], 'w-absent')).toBe(false)
  })
})

describe('onlineWorkerIdSet', () => {
  it('contains exactly the ids reported online', () => {
    const set = onlineWorkerIdSet([worker('w-on', true), worker('w-off', false), worker('w-on2', true)])
    expect([...set].sort()).toEqual(['w-on', 'w-on2'])
    // Absence conflates "known offline" with "not in the list" -- documented, and
    // why a caller needing that distinction must use workerOnlineState.
    expect(set.has('w-off')).toBe(false)
    expect(set.has('w-absent')).toBe(false)
  })
})
