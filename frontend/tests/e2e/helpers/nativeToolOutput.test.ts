import type { Locator, Page } from '@playwright/test'
import { runInNewContext } from 'node:vm'
import { describe, expect, it, vi } from 'vitest'
import { computedNativeToolOutput, copyNativeToolOutputPreview } from './nativeToolOutput'

// The Copy proof polls the clipboard. A short limit keeps the failing case fast.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return { ...actual, expect: actual.expect.configure({ timeout: 200 }) }
})

/** A page whose clipboard holds `clipboard()` after Copy, and that records each browser operation. */
function copyFixture(clipboard: () => string) {
  const events: string[] = []
  const copy = { click: async () => events.push('click') }
  const view = { hover: async () => events.push('hover'), getByRole: () => copy }
  const result = { locator: () => view } as unknown as Locator
  const page = {
    context: () => ({ grantPermissions: async (permissions: string[]) => events.push(`grant:${permissions.join(',')}`) }),
    evaluate: async (_operation: unknown, sentinel?: string) => {
      if (sentinel !== undefined) {
        events.push('clear')
        return undefined
      }
      return clipboard()
    },
  } as unknown as Page
  return { events, page, result }
}

describe('copyNativeToolOutputPreview', () => {
  it('grants the clipboard permissions before it clears the clipboard and clicks Copy', async () => {
    const f = copyFixture(() => 'native preview')
    await copyNativeToolOutputPreview(f.page, f.result, 'native preview')
    expect(f.events).toEqual(['grant:clipboard-read,clipboard-write', 'clear', 'hover', 'click'])
  })

  it('refuses empty preview text before it grants a permission', async () => {
    const f = copyFixture(() => '')
    await expect(copyNativeToolOutputPreview(f.page, f.result, '')).rejects.toThrow('nonempty native preview text')
    expect(f.events).toEqual([])
  })

  it('fails when Copy leaves other text in the clipboard', async () => {
    const f = copyFixture(() => 'native preview with another tail')
    // The poll ends with the failure of its `toEqual` check on the exact copied text.
    await expect(copyNativeToolOutputPreview(f.page, f.result, 'native preview')).rejects.toThrow(/toEqual/)
    expect(f.events).toEqual(['grant:clipboard-read,clipboard-write', 'clear', 'hover', 'click'])
  })
})

describe('computedNativeToolOutput', () => {
  it('requires real execution to create complete middle and final markers', () => {
    const output = computedNativeToolOutput({ prefix: 'OutputFileProbe', lineCount: 5, padding: 0 })
    expect(output.source).not.toContain(output.omittedMarker)
    expect(output.source).not.toContain(output.lastMarker)
    expect(output.source).not.toContain(output.lineMarker)
    expect(output.lineMarker).toBe('OutputFileProbe-line-')
    expect(output.text.split('\n').slice(0, -1).every(line => line.startsWith(output.lineMarker))).toBe(true)
    expect(runInNewContext(`${output.source}\ncompleteOutput`, {}, { timeout: 1000 })).toBe(output.text)
    expect(output.text.split('\n')).toEqual([
      'OutputFileProbe-line-0:',
      'OutputFileProbe-line-1:',
      'OutputFileProbe-line-2:-middle-77',
      'OutputFileProbe-line-3:',
      'OutputFileProbe-line-4:',
      'OutputFileProbe-complete-42',
    ])
  })

  it('creates distinct outputs without a supplied prefix', () => {
    const first = computedNativeToolOutput()
    const second = computedNativeToolOutput()
    expect(first.text).not.toBe(second.text)
    expect(first.text.split('\n')).toHaveLength(3001)
    expect(first.text).toContain(first.omittedMarker)
  })

  it('preserves a large complete output beyond native excerpt limits', () => {
    const output = computedNativeToolOutput({ prefix: 'LargeOutputFile', lineCount: 20_000, padding: 100 })
    expect(output.text.length).toBeGreaterThan(2_000_000)
    expect(runInNewContext(`${output.source}\ncompleteOutput`, {}, { timeout: 1000 })).toBe(output.text)
    expect(output.source).not.toContain(output.omittedMarker)
  })

  it.each(['', '1prefix', 'a-b', 'a b', 'a\0b', 'a'.repeat(81)])('rejects an invalid prefix %j', (prefix) => {
    expect(() => computedNativeToolOutput({ prefix })).toThrow('prefix')
  })

  it.each([0, -1, 2, 20_001, 3.5, Number.NaN, Number.POSITIVE_INFINITY])('rejects an invalid line count %j', (lineCount) => {
    expect(() => computedNativeToolOutput({ lineCount })).toThrow('lines')
  })

  it.each([-1, 101, 1.5, Number.NaN, Number.POSITIVE_INFINITY])('rejects invalid padding %j', (padding) => {
    expect(() => computedNativeToolOutput({ padding })).toThrow('padding')
  })

  it('accepts the smallest complete output with one distinct middle line', () => {
    const output = computedNativeToolOutput({ prefix: 'A', lineCount: 3, padding: 0 })
    expect(output.firstMarker).toBe('A-line-0:')
    expect(output.omittedMarker).toBe('A-line-1:-middle-77')
    expect(output.lastMarker).toBe('A-complete-42')
    expect(output.text).toBe('A-line-0:\nA-line-1:-middle-77\nA-line-2:\nA-complete-42')
  })
})
