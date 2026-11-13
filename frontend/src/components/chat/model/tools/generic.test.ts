import type { GenericToolRequest } from './generic'
import { describe, expect, it } from 'vitest'
import { prettifyJson } from '~/lib/jsonFormat'
import { genericArgsText, unknownContentText } from './generic'

/** A request whose one argument counts how often a formatter reads it. */
function countingRequest(): { request: GenericToolRequest, reads: () => number } {
  let reads = 0
  const args = {
    get query(): string {
      reads++
      return 'needle'
    },
  }
  return {
    request: { args },
    reads: () => reads,
  }
}

describe('genericArgsText', () => {
  it('answers the text that the provider stated, as it is', () => {
    expect(genericArgsText({
      args: { query: 'x' },
      argsText: 'raw text',
    })).toBe('raw text')
  })

  it('answers an empty stated text rather than formatting the arguments', () => {
    expect(genericArgsText({
      args: { query: 'x' },
      argsText: '',
    })).toBe('')
  })

  it('formats the arguments as JSON when the provider stated no text', () => {
    const args = { query: 'needle', limit: 3 }

    expect(genericArgsText({ args })).toBe(prettifyJson(args))
  })

  it('answers nothing for a request with no arguments', () => {
    expect(genericArgsText({ args: {} })).toBe('')
  })

  it('formats the arguments of one request once, however often a reader asks', () => {
    const { request, reads } = countingRequest()

    const first = genericArgsText(request)
    const readsAfterFirst = reads()
    expect(first).toContain('needle')
    expect(readsAfterFirst).toBeGreaterThan(0)

    expect(genericArgsText(request)).toBe(first)
    expect(reads()).toBe(readsAfterFirst)
  })

  it('formats a new request again, because it is a new revision of the row', () => {
    const { request, reads } = countingRequest()
    genericArgsText(request)
    const readsAfterFirst = reads()

    expect(genericArgsText({ args: request.args })).toContain('needle')
    expect(reads()).toBeGreaterThan(readsAfterFirst)
  })
})

/** An unknown content item whose raw value counts how often a formatter reads it. */
function countingUnknownItem(): { item: { type: 'unknown', raw: unknown }, reads: () => number } {
  let reads = 0
  const raw = {
    get payload(): string {
      reads++
      return 'opaque'
    },
  }
  return {
    item: { type: 'unknown', raw },
    reads: () => reads,
  }
}

describe('unknownContentText', () => {
  it('formats the raw value of the item as JSON', () => {
    const raw = { kind: 'future-block', size: 3 }

    expect(unknownContentText({ type: 'unknown', raw })).toBe(prettifyJson(raw))
  })

  it('formats a raw JSON text as prettifyJson does', () => {
    const raw = '{"kind":"future-block","size":3}'

    expect(unknownContentText({ type: 'unknown', raw })).toBe(prettifyJson(raw))
  })

  it.each([
    ['null', null],
    ['an empty string', ''],
    ['zero', 0],
  ])('formats %s as prettifyJson does, although the value is falsy', (_label, raw) => {
    expect(unknownContentText({ type: 'unknown', raw })).toBe(prettifyJson(raw))
  })

  it('formats one item once, however often a reader asks', () => {
    const { item, reads } = countingUnknownItem()

    const first = unknownContentText(item)
    const readsAfterFirst = reads()
    expect(first).toContain('opaque')
    expect(readsAfterFirst).toBe(1)

    expect(unknownContentText(item)).toBe(first)
    expect(reads()).toBe(readsAfterFirst)
  })

  it('formats a new item again, because it is a new revision of the row', () => {
    const { item, reads } = countingUnknownItem()
    unknownContentText(item)

    expect(unknownContentText({ type: 'unknown', raw: item.raw })).toContain('opaque')
    expect(reads()).toBe(2)
  })
})
