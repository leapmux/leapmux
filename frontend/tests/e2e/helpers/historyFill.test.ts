import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { ScriptedTurn } from './scriptedTurn'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fillerTurn, fillRows, splitRowsIntoTurns } from './historyFill'
import { sendScriptedTurn } from './scriptedTurn'

// Run the real assertion of a Page helper with a short unit-test deadline.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: actual.expect.configure({ timeout: 500 }),
  }
})

vi.mock('./scriptedTurn', () => ({
  sendScriptedTurn: vi.fn(async () => 0),
}))

describe('fillerTurn', () => {
  it('states its number in the prompt and the answer, and inserts the prompt', () => {
    expect(fillerTurn(3, false)).toEqual({ prompt: 'Filler 3: reply with its number.', answer: 'Filler answer 3.', entry: 'insert' })
  })

  it('adds reasoning to a turn with reasoning only', () => {
    expect(fillerTurn(4, true).reasoning).toBe('Thinking about filler 4.')
    expect(fillerTurn(4, false)).not.toHaveProperty('reasoning')
  })
})

describe('splitRowsIntoTurns', () => {
  it('splits a count with the fewest turns with reasoning', () => {
    expect(splitRowsIntoTurns(12, 3, 4)).toEqual({ plain: 4, reasoning: 0 })
    expect(splitRowsIntoTurns(13, 3, 4)).toEqual({ plain: 3, reasoning: 1 })
    expect(splitRowsIntoTurns(14, 3, 4)).toEqual({ plain: 2, reasoning: 2 })
  })

  it('splits no rows into no turn', () => {
    expect(splitRowsIntoTurns(0, 3, 4)).toEqual({ plain: 0, reasoning: 0 })
  })

  it('finds no split for a negative count, or for a count that the sizes cannot sum to', () => {
    expect(splitRowsIntoTurns(-1, 3, 4)).toBeNull()
    expect(splitRowsIntoTurns(5, 3, 4)).toBeNull()
    expect(splitRowsIntoTurns(7, 2, 4)).toBeNull()
  })

  it('refuses a turn size that is not a positive integer', () => {
    expect(() => splitRowsIntoTurns(6, 0, 4)).toThrow('positive integer')
    expect(() => splitRowsIntoTurns(6, 3, 1.5)).toThrow('positive integer')
  })
})

describe('fillRows', () => {
  const page = {} as Page
  const modelScript = {} as ModelScript

  /** A chat whose plain turn stores 3 rows and whose turn with reasoning stores 4, starting at `start` rows. */
  function simulatedChat(start: number) {
    let rows = start
    const sent: ScriptedTurn[] = []
    vi.mocked(sendScriptedTurn).mockImplementation(async (_page, _script, turn) => {
      sent.push(turn!)
      rows += turn!.reasoning === undefined
        ? 3
        : 4
      return 0
    })
    return {
      count: async () => rows,
      sent,
    }
  }

  beforeEach(() => {
    vi.mocked(sendScriptedTurn).mockReset()
  })

  it('measures each kind of turn, then fills the rest exactly, and returns the last turn', async () => {
    const chat = simulatedChat(5)

    const last = await fillRows({
      page,
      modelScript,
      count: chat.count,
      target: 30,
      firstTurn: 10,
    })

    expect(await chat.count()).toBe(30)
    expect(last).toBe(10 + chat.sent.length - 1)
    expect(chat.sent.map(turn => turn.prompt)).toEqual(chat.sent.map((_, index) => `Filler ${10 + index}: reply with its number.`))
    // One plain turn and one with reasoning measure the sizes first.
    expect(chat.sent.slice(0, 2).map(turn => turn.reasoning !== undefined)).toEqual([false, true])
  })

  it('sends no turn when the count is the target already', async () => {
    const chat = simulatedChat(30)

    expect(await fillRows({
      page,
      modelScript,
      count: chat.count,
      target: 30,
      firstTurn: 1,
    })).toBe(0)
    expect(chat.sent).toEqual([])
  })

  it('fails when the measuring turns pass the target', async () => {
    const chat = simulatedChat(26)

    await expect(fillRows({
      page,
      modelScript,
      count: chat.count,
      target: 30,
      firstTurn: 1,
    })).rejects.toThrow('split into turns')
  })
})
