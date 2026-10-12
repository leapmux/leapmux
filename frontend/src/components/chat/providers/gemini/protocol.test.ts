import { describe, expect, it } from 'vitest'
import { isGeminiCanceledToolSentence } from './protocol'

describe('isGeminiCanceledToolSentence', () => {
  it.each([
    'Tool "run_shell_command" was canceled by the user.',
    'Tool "replace" was canceled by the user.',
    'Tool "mcp_result_probe_inspect" was canceled by the user.',
  ])('accepts the exact sentence %j', (text) => {
    expect(isGeminiCanceledToolSentence(text)).toBe(true)
  })

  it.each([
    ['an empty text', ''],
    ['a sentence that states no tool', 'Tool "" was canceled by the user.'],
    ['a sentence with no final period', 'Tool "run_shell_command" was canceled by the user'],
    ['the British spelling', 'Tool "run_shell_command" was cancelled by the user.'],
    ['words after the sentence', 'Tool "run_shell_command" was canceled by the user. Try again.'],
    ['words before the sentence', 'Error: Tool "run_shell_command" was canceled by the user.'],
    ['a second line after the sentence', 'Tool "run_shell_command" was canceled by the user.\nTry again.'],
    ['a tool name across two lines', 'Tool "run\nshell" was canceled by the user.'],
    ['another error of the same tool', 'Tool "run_shell_command" not found in registry.'],
  ])('refuses %s', (_case, text) => {
    expect(isGeminiCanceledToolSentence(text)).toBe(false)
  })
})
