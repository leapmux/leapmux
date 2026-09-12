import { describe, expect, it } from 'vitest'
import { acpInputPaths, collectAcpToolText, flattenAcpContent } from './content'

describe('tool content (ACP)', () => {
  it('keeps valid path-array entries and falls back from empty aliases', () => {
    expect(acpInputPaths({ path: '', paths: [null, '', '/one', 0, '/two'] })).toEqual(['/one', '/two'])
    expect(acpInputPaths({ paths: [] })).toEqual([])
    expect(acpInputPaths({ filePath: '/one' })).toEqual(['/one'])
  })
  it.each([0, false])('preserves a scalar raw output of %j', (rawOutput) => {
    expect(collectAcpToolText({ rawOutput })).toBe(JSON.stringify(rawOutput))
  })

  it.each([0, false])('preserves an output field of %j', (output) => {
    expect(collectAcpToolText({ rawOutput: { output } })).toBe(JSON.stringify(output))
  })

  it('preserves structured output instead of coercing it to an object label', () => {
    expect(collectAcpToolText({ rawOutput: { output: { count: 0 } } })).toMatch(/"count"\s*:\s*0/)
  })

  it('preserves an unfamiliar raw result object', () => {
    expect(collectAcpToolText({ rawOutput: { answer: 42 } })).toMatch(/"answer"\s*:\s*42/)
  })

  it('preserves an image that also has a text caption', () => {
    const image = { type: 'image', data: 'image-data', mimeType: 'image/png', text: 'caption' }
    expect(flattenAcpContent([{ type: 'content', content: image }])).toEqual([image])
  })
})
