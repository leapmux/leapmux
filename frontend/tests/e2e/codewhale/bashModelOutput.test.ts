import { describe, expect, it } from 'vitest'
import { codewhaleBashModelMatches } from './bashModelOutput'

describe('codewhaleBashModelMatches', () => {
  it('accepts the exact second native projection while preserving the retained Worker bytes', () => {
    const raw = `[approval] This tool call required approval and was approved by the user before execution.\n\n${'native output\n'.repeat(4000)}`
    const before = raw
    const characters = Array.from(raw)
    const snippet = `${characters.slice(0, 2638).join('')}\n\n[... output truncated for context ...]\n\n${characters.slice(-1320).join('')}`
    const model = `[bash output compacted to protect context]\nSnippet: ${snippet}\n(Original: ${characters.length} chars, omitted: ${characters.length - 4000} chars.)`
    expect(codewhaleBashModelMatches(raw, model)).toBe(true)
    expect(raw).toBe(before)
    expect(codewhaleBashModelMatches(raw, model.replace('omitted:', 'lost:'))).toBe(false)
  })

  it('retains a short native result without another projection', () => {
    expect(codewhaleBashModelMatches('native short output', 'native short output')).toBe(true)
    expect(codewhaleBashModelMatches('native short output', 'different output')).toBe(false)
  })
})
