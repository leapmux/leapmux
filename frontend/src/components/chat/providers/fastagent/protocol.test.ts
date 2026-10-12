import { describe, expect, it } from 'vitest'
import { isFastAgentRefusalSentence } from './protocol'

describe('isFastAgentRefusalSentence', () => {
  it.each([
    'The user has declined permission to use this tool: acp_filesystem__write_text_file',
    'The user has permanently declined permission to use this tool: acp_terminal__execute',
    'The user has declined permission to use this tool: permission_probe__touch',
  ])('accepts the exact sentence %j', (text) => {
    expect(isFastAgentRefusalSentence(text)).toBe(true)
  })

  it.each([
    ['an empty text', ''],
    ['a sentence that states no tool', 'The user has declined permission to use this tool: '],
    ['a sentence with no tool and no space', 'The user has declined permission to use this tool:'],
    ['words before the sentence', 'Error: The user has declined permission to use this tool: acp_terminal__execute'],
    ['words after the tool', 'The user has declined permission to use this tool: acp_terminal__execute because it was late'],
    ['a second line after the sentence', 'The user has declined permission to use this tool: acp_terminal__execute\nTry again.'],
    ['a different adverb', 'The user has temporarily declined permission to use this tool: acp_terminal__execute'],
    ['the cancelled permission request', 'Permission request cancelled'],
    ['the file denial with no message', 'Permission denied for writing file: /w/notes.txt'],
  ])('refuses %s', (_case, text) => {
    expect(isFastAgentRefusalSentence(text)).toBe(false)
  })
})
