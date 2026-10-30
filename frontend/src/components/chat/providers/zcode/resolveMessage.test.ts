import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { input } from '../testUtils'
import { resolveZCodeMessage } from './resolveMessage'

const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/zcode_message_content_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: Array<{ name: string, original: Record<string, unknown>, supplemental: unknown, expected: Record<string, unknown> }>
}

describe('zcode supplemental input conformance', () => {
  it('loads a nonempty shared corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$name', ({ original, supplemental, expected }) => {
    const before = JSON.stringify({ original, supplemental })
    expect(resolveZCodeMessage({
      ...input(original),
      supplementalContent: supplemental,
    })).toEqual(expected)
    expect(JSON.stringify({ original, supplemental })).toBe(before)
  })
})

describe('resolveZCodeMessage', () => {
  it.each(['hello ', '🙂 '])('removes only a proven accepted prefix %s from a native response', (prefix) => {
    const original = {
      type: 'session.updated',
      payload: { content: `${prefix}world`, querySource: 'main_turn', stopReason: 'stop' },
      future: 42,
    }
    const supplementalContent = {
      zcodeResponseText: { acceptedPrefix: prefix },
    }
    const before = JSON.stringify({ original, supplementalContent })
    expect(resolveZCodeMessage({
      ...input(original),
      supplementalContent,
    }))
      .toEqual({
        ...original,
        payload: { ...original.payload, content: 'world' },
      })
    expect(JSON.stringify({ original, supplementalContent })).toBe(before)
  })

  it.each([null, 3, {}, 'different'])('ignores an invalid or contradictory accepted prefix %j', (acceptedPrefix) => {
    const original = {
      type: 'session.updated',
      payload: { content: 'answer', querySource: 'main_turn', stopReason: 'stop' },
    }
    expect(resolveZCodeMessage({
      ...input(original),
      supplementalContent: {
        zcodeResponseText: { acceptedPrefix },
      },
    })).toEqual(original)
  })

  it('keeps a fully represented response empty for classification', () => {
    const original = {
      type: 'session.updated',
      payload: { content: 'answer', querySource: 'main_turn', stopReason: 'stop' },
    }
    expect(resolveZCodeMessage({
      ...input(original),
      supplementalContent: {
        zcodeResponseText: { acceptedPrefix: 'answer' },
      },
    }))
      .toEqual({
        ...original,
        payload: { ...original.payload, content: '' },
      })
  })
})
