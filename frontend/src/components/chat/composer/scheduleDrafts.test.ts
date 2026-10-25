import { createMemo, createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as storage from '~/lib/browserStorage'
import { flushStorageWrites, localStorageLoad, localStorageStore, PREFIX_COMPOSER_SCHEDULE_DRAFT, setStorageAccountForTests } from '~/lib/browserStorage'
import { deferred } from '~/test-support/async'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { useTestStorage } from '~/test-support/persistentStorage'
import { queueEditDraftKey } from './draftIdentity'
import { clearScheduleDraft, forgetAgentScheduleDrafts, getScheduleDraft, hydrateScheduleDraft, scheduleDraftEpoch, setScheduleDraft } from './scheduleDrafts'

useTestStorage()

const KEYS = ['agent-1', 'agent-10', queueEditDraftKey('agent-1', 'input-1'), queueEditDraftKey('agent-10', 'input-1')]

afterEach(() => {
  for (const key of KEYS)
    clearScheduleDraft(key)
})

describe('scheduleDrafts', () => {
  it('holds no draft for a key that never received one', () => {
    expect(getScheduleDraft('agent-1')).toBeUndefined()
  })

  it('keeps a cleared schedule as a draft of its own, distinct from no draft', () => {
    setScheduleDraft('agent-1', '')
    expect(getScheduleDraft('agent-1')).toBe('')
    clearScheduleDraft('agent-1')
    expect(getScheduleDraft('agent-1')).toBeUndefined()
  })

  it('keeps the ordinary draft and an edit draft of one agent apart', () => {
    setScheduleDraft('agent-1', '2030-01-01T18:45')
    setScheduleDraft(queueEditDraftKey('agent-1', 'input-1'), '2030-01-02T18:45')
    expect(getScheduleDraft('agent-1')).toBe('2030-01-01T18:45')
    expect(getScheduleDraft(queueEditDraftKey('agent-1', 'input-1'))).toBe('2030-01-02T18:45')
  })

  it('notifies a reader of the key that changes', () => {
    createRoot((dispose) => {
      const draft = createMemo(() => getScheduleDraft('agent-1'))
      expect(draft()).toBeUndefined()
      setScheduleDraft('agent-1', '2030-01-01T18:45')
      expect(draft()).toBe('2030-01-01T18:45')
      clearScheduleDraft('agent-1')
      expect(draft()).toBeUndefined()
      dispose()
    })
  })

  it('forgets every draft of one agent and no draft of an agent whose id it prefixes', async () => {
    for (const key of KEYS)
      setScheduleDraft(key, '2030-01-01T18:45')
    await forgetAgentScheduleDrafts('agent-1')
    expect(getScheduleDraft('agent-1')).toBeUndefined()
    expect(getScheduleDraft(queueEditDraftKey('agent-1', 'input-1'))).toBeUndefined()
    expect(getScheduleDraft('agent-10')).toBe('2030-01-01T18:45')
    expect(getScheduleDraft(queueEditDraftKey('agent-10', 'input-1'))).toBe('2030-01-01T18:45')
  })

  it('forgets nothing for an agent that holds no draft', async () => {
    setScheduleDraft('agent-10', '2030-01-01T18:45')
    await forgetAgentScheduleDrafts('agent-1')
    expect(getScheduleDraft('agent-10')).toBe('2030-01-01T18:45')
  })
})

describe('schedule draft persistence', () => {
  const persisted = (key: string) => `${PREFIX_COMPOSER_SCHEDULE_DRAFT}${key}` as const

  it('restores ordinary schedules and explicitly cleared edit schedules', async () => {
    const edit = queueEditDraftKey('agent-1', 'input-1')
    setScheduleDraft('agent-1', '2030-01-01T18:45')
    setScheduleDraft(edit, '')
    await flushStorageWrites()
    setStorageAccountForTests('other-schedule-account')
    expect(getScheduleDraft('agent-1')).toBeUndefined()
    setStorageAccountForTests(TEST_USER_ID)
    await Promise.all([hydrateScheduleDraft('agent-1'), hydrateScheduleDraft(edit)])
    expect(getScheduleDraft('agent-1')).toBe('2030-01-01T18:45')
    expect(getScheduleDraft(edit)).toBe('')
  })

  it('keeps a newer edit when an earlier hydration completes', async () => {
    const read = deferred<unknown>()
    const spy = vi.spyOn(storage, 'localStorageLoad').mockReturnValueOnce(read.promise)
    const hydration = hydrateScheduleDraft('agent-1')
    setScheduleDraft('agent-1', '2030-02-01T18:45')
    read.resolve('2030-01-01T18:45')
    await hydration
    expect(getScheduleDraft('agent-1')).toBe('2030-02-01T18:45')
    spy.mockRestore()
  })

  it('keeps a clear when an earlier hydration completes', async () => {
    const read = deferred<unknown>()
    const spy = vi.spyOn(storage, 'localStorageLoad').mockReturnValueOnce(read.promise)
    const hydration = hydrateScheduleDraft('agent-1')
    clearScheduleDraft('agent-1')
    read.resolve('2030-01-01T18:45')
    await hydration
    expect(getScheduleDraft('agent-1')).toBeUndefined()
    expect(await localStorageLoad(persisted('agent-1'))).toBeUndefined()
    spy.mockRestore()
  })

  it('rejects a hydration from the previous storage account', async () => {
    const read = deferred<unknown>()
    const spy = vi.spyOn(storage, 'localStorageLoad').mockReturnValueOnce(read.promise)
    const hydration = hydrateScheduleDraft('agent-1')
    const before = scheduleDraftEpoch()
    setStorageAccountForTests('other-schedule-account')
    setScheduleDraft('agent-1', '2031-01-01T18:45')
    read.resolve('2030-01-01T18:45')
    await hydration
    expect(scheduleDraftEpoch()).toBeGreaterThan(before)
    expect(getScheduleDraft('agent-1')).toBe('2031-01-01T18:45')
    spy.mockRestore()
  })

  it('coalesces concurrent hydrations and accepts a missing override', async () => {
    const read = deferred<unknown>()
    const spy = vi.spyOn(storage, 'localStorageLoad').mockReturnValueOnce(read.promise)
    const first = hydrateScheduleDraft('agent-1')
    expect(hydrateScheduleDraft('agent-1')).toBe(first)
    expect(spy).toHaveBeenCalledTimes(1)
    read.resolve(undefined)
    await first
    expect(getScheduleDraft('agent-1')).toBeUndefined()
    spy.mockRestore()
  })

  it('removes durable edits that were never hydrated and preserves neighboring agents', async () => {
    const retired = queueEditDraftKey('agent-1', 'unloaded')
    const neighbor = queueEditDraftKey('agent-10', 'unloaded')
    localStorageStore(persisted(retired), '2030-01-01T18:45')
    localStorageStore(persisted(neighbor), '')
    await forgetAgentScheduleDrafts('agent-1')
    await expect.poll(() => localStorageLoad(persisted(retired))).toBeUndefined()
    expect(await localStorageLoad(persisted(neighbor))).toBe('')
  })
})
