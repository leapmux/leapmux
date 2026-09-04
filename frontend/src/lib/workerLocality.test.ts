import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import { describe, expect, it } from 'vitest'
import { isLocalWorker } from './workerLocality'

function worker(id: string, autoRegistered: boolean): Worker {
  return { id, autoRegistered } as Worker
}

const WORKERS = [worker('local', true), worker('remote', false)]

describe('isLocalWorker', () => {
  it('accepts the auto-registered worker of a solo desktop', () => {
    expect(isLocalWorker(WORKERS, 'local', true)).toBe(true)
  })

  it('refuses a registered remote worker even on a solo desktop', () => {
    expect(isLocalWorker(WORKERS, 'remote', true)).toBe(false)
  })

  it('refuses every worker when the shell is not solo', () => {
    expect(isLocalWorker(WORKERS, 'local', false)).toBe(false)
    expect(isLocalWorker(WORKERS, 'remote', false)).toBe(false)
  })

  it('refuses a worker id the list does not mention', () => {
    expect(isLocalWorker(WORKERS, 'gone', true)).toBe(false)
  })

  it('refuses an empty worker id', () => {
    expect(isLocalWorker(WORKERS, '', true)).toBe(false)
  })

  it('refuses when the list is empty', () => {
    expect(isLocalWorker([], 'local', true)).toBe(false)
  })
})
