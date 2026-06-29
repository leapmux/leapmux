import { describe, expect, it } from 'vitest'
import { ansiSyncTokenize } from './ansiTokenize'

// The ANSI escape byte (0x1B), built via fromCharCode so the source file stays plain ASCII.
const ESC = String.fromCharCode(27)

describe('ansiSyncTokenize', () => {
  it('tokenizes ansi content into colored spans on the main thread', () => {
    // ansi is a Shiki special language with no bundled grammar, so the async worker
    // returns plain -- this synchronous path is what keeps `.log` surfaces colored.
    const tokens = ansiSyncTokenize('ansi', `${ESC}[32mgreen${ESC}[0m plain`)
    expect(tokens).not.toBeNull()
    // One line; the escape sequences are consumed and become per-token colors.
    expect(tokens).toHaveLength(1)
    const line = tokens![0]
    expect(line.map(t => t.content).join('')).toBe('green plain')
    // The colored segment carries the dual-theme CSS variables the wrapper themes.
    const colored = line.find(t => t.content === 'green')!
    expect(colored.htmlStyle).toMatchObject({ '--shiki-light': expect.any(String), '--shiki-dark': expect.any(String) })
  })

  it('returns null for a non-ansi language so the caller falls through to the worker', () => {
    expect(ansiSyncTokenize('json', '{"a":1}')).toBeNull()
    expect(ansiSyncTokenize('typescript', 'const x = 1')).toBeNull()
    expect(ansiSyncTokenize('bash', 'echo hi')).toBeNull()
  })

  it('tokenizes empty ansi input without throwing', () => {
    const tokens = ansiSyncTokenize('ansi', '')
    expect(tokens).not.toBeNull()
  })
})
