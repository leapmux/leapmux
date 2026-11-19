import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { describe, expect, it, vi } from 'vitest'
import { BackgroundTaskRole } from '~/generated/proto/leapmux/v1/agent_pb'
import { NO_FILE_IMAGES } from './fileImageResolver'
import { imageActionsFrom, isDisplayHeld, reviewActionsFrom, shouldPauseSyntaxHighlighting, subagentsFrom } from './renderContext'

// The three assemblies are the ONLY way the narrow capabilities are built, so their
// edge answers are the ones every isolated mount inherits. For the subagent
// navigation, a host that supplies nothing gets no capability, and an optional member
// stays optional rather than silently becoming a no-op. The image actions require
// each member that a row always has.

describe('subagentsFrom', () => {
  it('answers undefined when the host supplies neither member', () => {
    expect(subagentsFrom(undefined)).toBeUndefined()
    expect(subagentsFrom({})).toBeUndefined()
  })

  it('keeps open OPTIONAL when only the row lookup is supplied', () => {
    const navigation = subagentsFrom({
      backgroundTask: () => undefined,
    })
    expect(navigation?.open).toBeUndefined()
    expect(navigation?.row('k')).toBeUndefined()
  })

  it('carries both members through when both are supplied', () => {
    const item: BackgroundTaskItem = {
      role: BackgroundTaskRole.TASK_OUTCOME,
      rowKey: 'k',
      kind: 'subagent',
      title: 'Inspect it',
      activity: 'Inspecting',
      status: 'running',
    }
    const open = vi.fn()
    const navigation = subagentsFrom({
      backgroundTask: key => key === 'k'
        ? item
        : undefined,
      openSubagent: open,
    })
    expect(navigation?.row('k')).toBe(item)
    navigation?.open?.(item)
    expect(open).toHaveBeenCalledWith(item)
  })
})

describe('imageActionsFrom', () => {
  it('passes the image files and the load gates of the host through', () => {
    const actions = imageActionsFrom({
      files: NO_FILE_IMAGES,
      deferLoad: () => true,
      premeasurePass: () => true,
    })

    expect(actions.files).toBe(NO_FILE_IMAGES)
    expect(actions.deferLoad()).toBe(true)
    expect(actions.premeasurePass()).toBe(true)
  })

  it('reads the load gates through the host, lazily', () => {
    let deferred = true
    let premeasure = true
    const actions = imageActionsFrom({
      files: NO_FILE_IMAGES,
      deferLoad: () => deferred,
      premeasurePass: () => premeasure,
    })
    expect(actions.deferLoad()).toBe(true)
    expect(actions.premeasurePass()).toBe(true)
    deferred = false
    premeasure = false
    expect(actions.deferLoad()).toBe(false)
    expect(actions.premeasurePass()).toBe(false)
  })

  it('opens an image through the open handler of the host', () => {
    const open = vi.fn()
    const actions = imageActionsFrom({
      files: NO_FILE_IMAGES,
      openImage: open,
      deferLoad: () => false,
      premeasurePass: () => false,
    })

    actions.openImage?.({ index: 2, filePath: '/p.png' })

    expect(open).toHaveBeenCalledExactlyOnceWith({ index: 2, filePath: '/p.png' })
  })

  it('gives no open handler for a host that has none, so the image view draws no button', () => {
    const actions = imageActionsFrom({
      files: NO_FILE_IMAGES,
      deferLoad: () => false,
      premeasurePass: () => false,
    })

    expect(actions.openImage).toBeUndefined()
  })
})

describe('reviewActionsFrom', () => {
  it('approves through the handler with the row\'s message id', async () => {
    const approve = vi.fn(async (_messageId: string, _reviewId: string) => {})
    const actions = reviewActionsFrom({
      messageId: () => 'm1',
      approveAutoReviewDenial: () => approve,
    })
    await actions.approveDenied('r1')
    expect(approve).toHaveBeenCalledWith('m1', 'r1')
  })

  it('reads the message id and the handler at call time', async () => {
    let messageId = 'm1'
    let handler: ((messageId: string, reviewId: string) => Promise<void>) | undefined
    const actions = reviewActionsFrom({
      messageId: () => messageId,
      approveAutoReviewDenial: () => handler,
    })
    const approve = vi.fn(async () => {})
    handler = approve
    messageId = 'm2'
    await actions.approveDenied('r1')
    expect(approve).toHaveBeenCalledWith('m2', 'r1')
  })

  it('fails a call after the host lost its handler, so the button shows the failure', async () => {
    const actions = reviewActionsFrom({
      messageId: () => 'm1',
      approveAutoReviewDenial: () => undefined,
    })
    await expect(actions.approveDenied('r1')).rejects.toThrow('This agent cannot record the approval now.')
  })

  it('passes a refusal of the handler through', async () => {
    const refuse = async () => {
      throw new Error('refused')
    }
    const actions = reviewActionsFrom({
      messageId: () => 'm1',
      approveAutoReviewDenial: () => refuse,
    })
    await expect(actions.approveDenied('r1')).rejects.toThrow('refused')
  })
})

describe('shouldPauseSyntaxHighlighting', () => {
  it.each([
    [
      'the hidden premeasure pass',
      { premeasureMode: true },
    ],
    [
      'a scroll',
      {
        syntaxHighlightingPaused: () => true,
      },
    ],
    [
      'a text selection',
      {
        textSelectionActive: () => true,
      },
    ],
  ] as const)('pauses during %s', (_label, context) => {
    expect(shouldPauseSyntaxHighlighting(context)).toBe(true)
  })

  it.each([
    ['no context', undefined],
    ['a context that sets no flag', {}],
    [
      'a context whose flags are all false',
      {
        premeasureMode: false,
        syntaxHighlightingPaused: () => false,
        textSelectionActive: () => false,
      },
    ],
  ] as const)('does not pause with %s', (_label, context) => {
    expect(shouldPauseSyntaxHighlighting(context)).toBe(false)
  })
})

// A held display keeps the DOM that a body shows. The hidden premeasure copy shows
// no reader anything, so it holds nothing: it takes the final HTML at once.
describe('isDisplayHeld', () => {
  it.each([
    [
      'a scroll',
      {
        syntaxHighlightingPaused: () => true,
      },
    ],
    [
      'a text selection',
      {
        textSelectionActive: () => true,
      },
    ],
  ] as const)('holds the display during %s', (_label, context) => {
    expect(isDisplayHeld(context)).toBe(true)
  })

  it.each([
    ['no context', undefined],
    ['a context that sets no flag', {}],
    [
      'the hidden premeasure pass alone',
      { premeasureMode: true },
    ],
    [
      'a context whose flags are all false',
      {
        syntaxHighlightingPaused: () => false,
        textSelectionActive: () => false,
      },
    ],
  ] as const)('does not hold the display with %s', (_label, context) => {
    expect(isDisplayHeld(context)).toBe(false)
  })
})
