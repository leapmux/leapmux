import type { Page } from '@playwright/test'
import type { NativeToolRowIdResolver } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { selectedAgentTabId } from './nativeScenario'
import { nativeToolRowId } from './nativeToolRowId'

vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  selectedAgentTabId: vi.fn(async () => 'selected-child'),
}))

const page = {} as Page

beforeEach(() => {
  vi.mocked(selectedAgentTabId).mockClear()
})

describe('nativeToolRowId', () => {
  it.each(['model-call', '', '  ', 'a"b\\c', 'raw\0call'])('keeps the raw ID without a provider resolver or a tab lookup: %j', async (callId) => {
    expect(await nativeToolRowId({ page }, callId)).toBe(callId)
    expect(selectedAgentTabId).not.toHaveBeenCalled()
  })

  it('resolves the exact selected child while preserving the model call ID', async () => {
    const resolveToolRowId = vi.fn(async () => 'native-child-part')
    expect(await nativeToolRowId({ page, resolveToolRowId }, 'model-read')).toBe('native-child-part')
    expect(resolveToolRowId).toHaveBeenCalledExactlyOnceWith({ callId: 'model-read', agentId: 'selected-child' })
    expect(selectedAgentTabId).toHaveBeenCalledOnce()
  })

  it.each(['', '  '])('refuses a missing model ID before it reads the selected tab: %j', async (callId) => {
    const resolveToolRowId = vi.fn(async () => 'native-part')
    await expect(nativeToolRowId({ page, resolveToolRowId }, callId)).rejects.toThrow('requires a model call ID')
    expect(selectedAgentTabId).not.toHaveBeenCalled()
    expect(resolveToolRowId).not.toHaveBeenCalled()
  })

  it.each(['', '  ', undefined, null, 0])('refuses an absent or invalid native row ID: %j', async (value) => {
    const resolveToolRowId = (async () => value) as NativeToolRowIdResolver
    await expect(nativeToolRowId({ page, resolveToolRowId }, 'model-call')).rejects.toThrow('supplied no native tool row ID')
  })

  it('preserves a provider ownership failure', async () => {
    const failure = new Error('The native call has conflicting owners.')
    const resolveToolRowId = vi.fn(async () => {
      throw failure
    })
    await expect(nativeToolRowId({ page, resolveToolRowId }, 'model-call')).rejects.toBe(failure)
  })

  it.each(['\0', 'native\0part'])('refuses NUL in a resolved ID before a browser locator uses it: %j', async (rowId) => {
    const resolveToolRowId = async () => rowId
    await expect(nativeToolRowId({ page, resolveToolRowId }, 'model-call')).rejects.toThrow('cannot contain NUL')
  })

  it('keeps the exact native ID for the existing selector escape helper', async () => {
    const resolveToolRowId = async () => 'native"part\\id'
    expect(await nativeToolRowId({ page, resolveToolRowId }, 'model-call')).toBe('native"part\\id')
  })
})
