import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { describe, expect, it, vi } from 'vitest'
import { imageActionsFrom, subagentsFrom } from './renderContext'

// The two assemblies are the ONLY way the narrow capabilities are built, so their
// edge answers are the ones every isolated mount inherits: a host that supplies
// nothing gets no capability, and an optional member stays optional rather than
// silently becoming a no-op.

describe('subagentsFrom', () => {
  it('answers undefined when the host supplies neither member', () => {
    expect(subagentsFrom(undefined)).toBeUndefined()
    expect(subagentsFrom({})).toBeUndefined()
  })

  it('keeps open OPTIONAL when only the row lookup is supplied', () => {
    const navigation = subagentsFrom({ backgroundTask: () => undefined })
    expect(navigation?.open).toBeUndefined()
    expect(navigation?.row('k')).toBeUndefined()
  })

  it('carries both members through when both are supplied', () => {
    const item: BackgroundTaskItem = { rowKey: 'k', kind: 'subagent', title: 'Inspect it', activity: 'Inspecting', status: 'running' }
    const open = vi.fn()
    const navigation = subagentsFrom({ backgroundTask: key => key === 'k' ? item : undefined, openSubagent: open })
    expect(navigation?.row('k')).toBe(item)
    navigation?.open?.(item)
    expect(open).toHaveBeenCalledWith(item)
  })
})

describe('imageActionsFrom', () => {
  it('answers undefined when the host supplies no member', () => {
    expect(imageActionsFrom(undefined)).toBeUndefined()
    expect(imageActionsFrom({})).toBeUndefined()
  })

  it('defaults the absent halves rather than dropping the capability', async () => {
    const open = vi.fn()
    const actions = imageActionsFrom({ openImage: open })!
    expect(await actions.cachedFileImage('/p.png')).toBeUndefined()
    expect(await actions.loadFileImage('/p.png')).toBeUndefined()
    expect(actions.deferLoad()).toBe(false)
    expect(actions.premeasurePass()).toBe(false)
    actions.openImage({ index: 0 })
    expect(open).toHaveBeenCalledWith({ index: 0 })
  })

  it('reads the load gates through the host, lazily', () => {
    // The gates alone are not a capability: a host with no channel and no opener
    // has nothing to defer, and the adapter answers undefined for it.
    expect(imageActionsFrom({ deferLoad: () => true })).toBeUndefined()
    let deferred = true
    const actions = imageActionsFrom({ openImage: () => {}, deferLoad: () => deferred })!
    expect(actions.deferLoad()).toBe(true)
    deferred = false
    expect(actions.deferLoad()).toBe(false)
  })

  // The premeasure-only case: a hidden measurement mount supplies no file channel
  // and no opener, and still needs the decode hint the pass requires.
  it('builds from a premeasure hint alone', () => {
    const actions = imageActionsFrom({ premeasurePass: () => true })
    expect(actions?.premeasurePass()).toBe(true)
  })
})
