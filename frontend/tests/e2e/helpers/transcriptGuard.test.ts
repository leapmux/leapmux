import type { Page } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTranscriptGuard, isRawFrameText, runTranscriptGuard } from './transcriptGuard'

const state = vi.hoisted(() => ({
  unrendered: 0,
  spinners: 0,
  texts: [] as string[],
  selectors: [] as string[],
}))

vi.mock('@playwright/test', () => ({
  expect: Object.assign((locator: { count: number }) => ({
    toHaveCount: async (count: number) => { expect(locator.count).toBe(count) },
  }), {
    poll: (read: () => Promise<unknown>) => ({
      toEqual: async (expected: unknown) => { expect(await read()).toEqual(expected) },
    }),
  }),
}))

vi.mock('./ui', () => ({
  visibleOnly: (locator: unknown) => locator,
  messageBubbles: () => ({
    and: () => ({
      getByTestId: () => ({
        allTextContents: async () => state.texts,
      }),
    }),
  }),
  chatScrollContainer: () => ({
    locator: (selector: string) => {
      state.selectors.push(selector)
      return { count: state.spinners }
    },
  }),
}))

const page = {
  getByTestId: (id: string) => {
    state.selectors.push(id)
    return { count: state.unrendered }
  },
  locator: (selector: string) => { state.selectors.push(selector) },
} as unknown as Page

beforeEach(() => {
  state.unrendered = 0
  state.spinners = 0
  state.texts = []
  state.selectors = []
})

describe('isRawFrameText', () => {
  it.each([
    'type',
    'method',
    'event',
    'subtype',
    'sessionUpdate',
    'jsonrpc',
  ])('recognizes the %s discriminator in a whole frame', (key) => {
    expect(isRawFrameText(` \n${JSON.stringify({ [key]: 'notice', value: null })}\t`)).toBe(true)
  })

  it.each([
    'prose',
    '{"a":1}',
    '[]',
    'null',
    '{invalid}',
    '{"a":1,"b":2}',
    '{"type":1,"b":2}',
    'prefix {"type":"notice","b":2}',
    '{"type":"notice","b":2} suffix',
  ])('refuses text that is not a native frame: %s', (text) => {
    expect(isRawFrameText(text)).toBe(false)
  })
})

describe('createTranscriptGuard', () => {
  it('records a nonempty trimmed reason', () => {
    const guard = createTranscriptGuard()
    expect(guard.allowedReason).toBeUndefined()
    guard.allow('  this scenario tests an unknown frame  ')
    expect(guard.allowedReason).toBe('this scenario tests an unknown frame')
  })

  it.each(['', ' \n\t'])('refuses a blank reason: %j', (reason) => {
    expect(() => createTranscriptGuard().allow(reason)).toThrow('needs a reason')
  })
})

describe('runTranscriptGuard', () => {
  it('accepts a clean transcript and checks visible stored spinners', async () => {
    state.texts = ['A useful notice', '{"a":1}']
    await runTranscriptGuard(page, createTranscriptGuard(), { status: 'passed', expectedStatus: 'passed' })
    expect(state.selectors).toEqual([
      'unrendered-row',
      '[data-category="notification"], [data-category="result_divider"]',
      '[data-testid="notification-divider"][data-loading="true"]:visible',
    ])
  })

  it.each(['unrendered', 'spinners', 'raw'])('rejects a transcript with %s output', async (defect) => {
    if (defect === 'raw')
      state.texts = ['{"type":"notice","payload":{}}']
    else
      state[defect as 'unrendered' | 'spinners'] = 1
    await expect(runTranscriptGuard(page, createTranscriptGuard(), { status: 'passed', expectedStatus: 'passed' }))
      .rejects
      .toThrow(defect === 'raw'
        ? /type.*notice/
        : 'expected 1 to be +0')
  })

  it('skips after an unexpected test failure', async () => {
    await runTranscriptGuard(page, createTranscriptGuard(), { status: 'failed', expectedStatus: 'passed' })
    expect(state.selectors).toEqual([])
  })

  it('skips after an explicit exception', async () => {
    const guard = createTranscriptGuard()
    guard.allow('The scenario tests an unknown native frame.')
    await runTranscriptGuard(page, guard, { status: 'passed', expectedStatus: 'passed' })
    expect(state.selectors).toEqual([])
  })
})
