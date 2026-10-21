// @vitest-environment jsdom
import type { Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentInputState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { QUEUED_INPUT_ROW_TESTID, STEER_BUTTON_NAME, steerQueuedInput } from './steer'

class TestLocator {
  constructor(readonly elements: () => Element[]) {}

  filter(options: { hasText: string }) {
    return new TestLocator(() => this.elements().filter(element => element.textContent?.includes(options.hasText)))
  }

  getByRole(_role: string, options: { name: string }) {
    return new TestLocator(() => {
      const buttons = this.elements().flatMap(element => [...element.querySelectorAll('button')])
      return buttons.filter(element => element.textContent === options.name)
    })
  }

  and(other: TestLocator) {
    return new TestLocator(() => {
      const matching = new Set(other.elements())
      return this.elements().filter(element => matching.has(element))
    })
  }

  async click() {
    const element = this.elements()[0]
    if (!(element instanceof HTMLElement))
      throw new Error('The test locator has no clickable element.')
    element.click()
  }
}

vi.mock('./ui', () => ({ sendMessage: vi.fn(), assistantBubbles: vi.fn() }))
vi.mock('@playwright/test', () => ({
  expect: (locator: TestLocator) => ({
    toBeVisible: async () => expect(locator.elements().length).toBeGreaterThan(0),
    toHaveCount: async (count: number) => expect(locator.elements()).toHaveLength(count),
  }),
}))

afterEach(() => document.body.replaceChildren())

describe('steerQueuedInput', () => {
  it.each(['awaiting echo', 'removed', 'queued'] as const)('checks the row after the click leaves it %s', async (outcome) => {
    document.body.innerHTML = '<div data-testid="queued-input-test" data-input-state="1">Queued message<button>Steer</button></div>'
    const row = document.querySelector('[data-testid="queued-input-test"]')!
    row.querySelector('button')!.addEventListener('click', () => {
      if (outcome === 'removed')
        row.remove()
      else if (outcome === 'awaiting echo')
        row.setAttribute('data-input-state', String(AgentInputState.AWAITING_ECHO))
    })
    const page = {
      getByTestId: (id: RegExp) => new TestLocator(() => [...document.querySelectorAll('[data-testid]')]
        .filter(element => id.test(element.getAttribute('data-testid') ?? ''))),
      locator: (selector: string) => new TestLocator(() => [...document.querySelectorAll(selector)]),
    } as unknown as Page
    const result = steerQueuedInput(page, { message: 'Queued message', match: 'Queued message' })
    if (outcome === 'queued')
      await expect(result).rejects.toThrow('to have a length of +0')
    else
      await expect(result).resolves.toBeUndefined()
  })
})

describe('QUEUED_INPUT_ROW_TESTID', () => {
  it('matches a queued-input row test id', () => {
    expect(QUEUED_INPUT_ROW_TESTID.test('queued-input-abc123')).toBe(true)
  })

  // The row pattern drifted twice onto an unanchored form that matched more
  // than the row. The anchor is the fix, so it is pinned here.
  it('is anchored at the start of the test id', () => {
    expect(QUEUED_INPUT_ROW_TESTID.test('pre-queued-input-abc123')).toBe(false)
  })

  it('requires the dash that ends the row prefix', () => {
    expect(QUEUED_INPUT_ROW_TESTID.test('queued-input')).toBe(false)
    expect(QUEUED_INPUT_ROW_TESTID.test('queued-inputs-abc123')).toBe(false)
  })
})

describe('STEER_BUTTON_NAME', () => {
  // The helper finds the button by its role name. If the queue rewords the
  // action and this constant does not follow, the click finds nothing.
  it('is the name the input queue renders on its steer action', () => {
    const source = readFileSync(
      resolve(import.meta.dirname, '../../../src/components/chat/AgentInputQueue.tsx'),
      'utf8',
    )
    expect(source, `the input queue renders no "${STEER_BUTTON_NAME}" action`).toContain(`label="${STEER_BUTTON_NAME}"`)
  })
})
