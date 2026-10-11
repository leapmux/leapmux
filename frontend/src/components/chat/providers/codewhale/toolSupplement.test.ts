import { describe, expect, it } from 'vitest'
import { codewhaleToolOutputFiles } from './toolSupplement'

describe('codewhaleToolOutputFiles', () => {
  it.each([undefined, null, '', 0, [], {}, { outputFiles: [] }, { outputFiles: 'not a map' }])('refuses an absent or malformed artifact map: %j', (supplement) => {
    expect(codewhaleToolOutputFiles(supplement)).toBeUndefined()
  })

  it('keeps the actual full tool output identities without reading the original payload', () => {
    const outputFiles = { actual: 'data:image/png;base64,AAAA' }
    expect(codewhaleToolOutputFiles({ outputFiles })).toBe(outputFiles)
  })
})
