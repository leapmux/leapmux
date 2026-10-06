import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { recordingLocator } from '~/test-support/fakeLocator'
import { RAIL, RAIL_FILLER_MESSAGE, RAIL_FILLER_PREVIEW, seedOverflowingConversation } from './chatScrollRail'
import { sendScriptedTurn } from './scriptedTurn'
import { userBubbles, waitForAgentStarted } from './ui'

vi.mock('./scriptedTurn', () => ({ sendScriptedTurn: vi.fn(async () => 0) }))
vi.mock('./ui', () => ({ userBubbles: vi.fn(), waitForAgentStarted: vi.fn(async () => {}) }))

const script = {} as ModelScript

/** A page whose rail locator answers from `railShows`, with every step of the seed in `log`. */
function seedingPage(log: string[], railShows = true): Page {
  vi.mocked(waitForAgentStarted).mockImplementation(async () => {
    log.push('started')
  })
  vi.mocked(sendScriptedTurn).mockImplementation(async (_page, _script, turn) => {
    log.push(`turn ${turn?.answer}`)
    return 0
  })
  vi.mocked(userBubbles).mockReturnValue(recordingLocator('users', log))
  return {
    locator: (selector: string) => {
      expect(selector).toBe(RAIL)
      return recordingLocator('rail', log, () => railShows)
    },
  } as unknown as Page
}

beforeEach(() => {
  vi.mocked(sendScriptedTurn).mockReset()
  vi.mocked(waitForAgentStarted).mockReset()
  vi.mocked(userBubbles).mockReset()
})

describe('seedOverflowingConversation', () => {
  it('waits for the startup, sends one filler turn for each message, and requires each user bubble and the rail', async () => {
    const log: string[] = []
    await seedOverflowingConversation(seedingPage(log), script, 2)
    expect(log).toEqual(['started', 'turn ok', 'turn ok', 'users:to.have.count=2', 'rail:to.be.visible'])
    expect(vi.mocked(sendScriptedTurn).mock.calls.map(call => call[2]))
      .toEqual([{ prompt: RAIL_FILLER_MESSAGE, answer: 'ok' }, { prompt: RAIL_FILLER_MESSAGE, answer: 'ok' }])
  })

  it('sends one message by default', async () => {
    const log: string[] = []
    await seedOverflowingConversation(seedingPage(log), script)
    expect(log).toEqual(['started', 'turn ok', 'users:to.have.count=1', 'rail:to.be.visible'])
  })

  it('fails with the overflow reason when the rail does not show', async () => {
    await expect(seedOverflowingConversation(seedingPage([], false), script)).rejects.toThrow('the conversation overflows, so the rail shows')
  })

  it.each([0, -1, 1.5])('refuses %s messages before it sends anything', async (messages) => {
    const log: string[] = []
    await expect(seedOverflowingConversation(seedingPage(log), script, messages)).rejects.toThrow(RangeError)
    expect(log).toEqual([])
  })
})

describe('RAIL_FILLER_MESSAGE', () => {
  it('starts with the text that its preview card shows', () => {
    expect(RAIL_FILLER_MESSAGE.startsWith(RAIL_FILLER_PREVIEW)).toBe(true)
  })
})
