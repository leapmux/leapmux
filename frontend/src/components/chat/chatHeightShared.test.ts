import { describe, expect, it } from 'vitest'
import {
  COMMAND_INPUT_EXPAND_CHAR_THRESHOLD,
  commandInputNeedsExpansion,
  isMultiLineCommand,
} from './chatHeightShared'

describe('chatHeightShared command helpers', () => {
  it('detects commands with hard line breaks', () => {
    expect(isMultiLineCommand('echo one')).toBe(false)
    expect(isMultiLineCommand('echo one\necho two')).toBe(true)
    expect(isMultiLineCommand('')).toBe(false)
    expect(isMultiLineCommand(null)).toBe(false)
    expect(isMultiLineCommand(undefined)).toBe(false)
  })

  it('requires expansion for multiline commands', () => {
    expect(commandInputNeedsExpansion('echo one\necho two')).toBe(true)
  })

  it('requires expansion for long single-line commands only above the threshold', () => {
    expect(commandInputNeedsExpansion('x'.repeat(COMMAND_INPUT_EXPAND_CHAR_THRESHOLD))).toBe(false)
    expect(commandInputNeedsExpansion('x'.repeat(COMMAND_INPUT_EXPAND_CHAR_THRESHOLD + 1))).toBe(true)
  })

  it('does not require expansion for empty or absent commands', () => {
    expect(commandInputNeedsExpansion('')).toBe(false)
    expect(commandInputNeedsExpansion(null)).toBe(false)
    expect(commandInputNeedsExpansion(undefined)).toBe(false)
  })
})
