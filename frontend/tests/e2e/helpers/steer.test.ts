import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { QUEUED_INPUT_ROW_TESTID, STEER_BUTTON_NAME } from './steer'

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
