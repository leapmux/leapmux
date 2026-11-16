import { describe, expect, it } from 'vitest'
import { toolCallFixture } from '~/test-support/toolCallFixture'
import { MESSAGE_UI_KEY } from '../messageUiKeys'
import { REQUEST_ROW_EXPAND_KEYS, rowExpandKey } from './toolRowState'
import { dispatchParts } from './tools/index'

describe('rowExpandKey', () => {
  // The request row and the pending row of an agent launch open its prompt.
  it('reads the key that a kind words of its own on a row that draws no result', () => {
    const parts = dispatchParts(toolCallFixture('agent', {
      request: { description: 'Fix it', prompt: 'Run the tests.' },
    }))
    expect(rowExpandKey(false, parts)).toBe(MESSAGE_UI_KEY.AGENT_PROMPT)
    expect(rowExpandKey(true, parts)).toBe(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
  })

  it('reads the result key for a kind with no key of its own', () => {
    const parts = dispatchParts(toolCallFixture('execute', {
      request: { command: 'make' },
    }))
    expect(rowExpandKey(false, parts)).toBe(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)
  })
})

describe('REQUEST_ROW_EXPAND_KEYS', () => {
  it('lists every key that a row which draws no result can read, once', () => {
    expect(REQUEST_ROW_EXPAND_KEYS).toEqual([
      MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED,
      MESSAGE_UI_KEY.AGENT_PROMPT,
    ])
  })
})
