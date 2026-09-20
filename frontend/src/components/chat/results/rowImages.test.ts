import type { ImageResultSource } from '~/lib/imageBlocks'
import { describe, expect, it } from 'vitest'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { failedResult, isGenericCall, unparsedResult } from '../model/toolCall'
import { imagesForRow, resultImages } from './rowImages'
import { quotableTextForRow } from './rowText'

const picture = (name: string): ImageResultSource => ({ mimeType: 'image/png', data: name })

describe('imagesForRow', () => {
  it('answers nothing for a row that is no tool row', () => {
    expect(imagesForRow(null)).toEqual([])
    expect(imagesForRow({ kind: 'user', text: 'hi', attachments: [] })).toEqual([])
  })

  it('lists a merged call\'s pictures in the order the row draws them', () => {
    const call = toolCallFixture('read', {
      images: [picture('own')],
      extraContent: [{ type: 'image', source: picture('extra') }],
    })
    expect(imagesForRow(toolRow(call)).map(p => p.data)).toEqual(['extra', 'own'])
  })

  // `ToolMessage` draws the RESULT body first, the extra content under it, and the
  // call's own images last. A list that led with the extra content stated an order no
  // row drew, so a tab opened from a result picture showed the extra one.
  //
  // NO single call states all three terms. Invariant I6 keeps a generic kind's
  // pictures inside `result.content`, and a generic kind is the only one whose result
  // holds content blocks -- so the first term and the last term belong to different
  // kinds. This case pins the first pair over a generic call and the case above pins
  // the second pair over a typed one. The extra content is the term they share, which
  // is what joins the two halves into the one order the row draws.
  it('lists a generic result\'s pictures before the extra content\'s', () => {
    const call = toolCallFixture('mcp', {
      extraContent: [{ type: 'image', source: picture('extra') }],
      result: { content: [{ type: 'image', source: picture('inline') }] },
    })
    expect(imagesForRow(toolRow(call)).map(p => p.data)).toEqual(['inline', 'extra'])
  })

  it('lists nothing for a request row paired with its result row', () => {
    const call = toolCallFixture('read', { images: [picture('own')] })
    expect(imagesForRow(toolRow(call, 'request', { result: true }))).toEqual([])
  })
})

describe('quotableTextForRow', () => {
  it('answers no prose for a tool row', () => {
    expect(quotableTextForRow(toolRow(toolCallFixture('read')))).toBeNull()
  })
})

/**
 * The generic trio keeps its pictures INSIDE the result's content blocks; every other
 * kind carries them on the call. `resultImages` reads the first half.
 *
 * It asks `isGenericCall`, which narrows the CALL. The kind predicate beside it narrows
 * only the discriminant it was read from, which leaves the call itself un-narrowed --
 * so the earlier form had to assert the call back, and an assertion here is exactly
 * what could pair a kind with another kind's result.
 */
describe('resultImages', () => {
  it.each(['unspecified', 'other', 'mcp'] as const)('reads the content pictures of a %s result', (kind) => {
    const call = toolCallFixture(kind, {
      result: { content: [{ type: 'image', source: picture('inline') }, { type: 'text', text: 'beside it' }] },
    })
    expect(resultImages(call).map(p => p.data)).toEqual(['inline'])
  })

  // A kind whose result declares no content blocks at all. The narrowing is what keeps
  // this from reading a field the result does not have.
  it('answers nothing for a kind that carries its pictures on the call', () => {
    expect(resultImages(toolCallFixture('read', { images: [picture('own')] }))).toEqual([])
  })

  it('answers nothing for a generic call that has not answered', () => {
    expect(resultImages(toolCallFixture('mcp'))).toEqual([])
  })

  // `typedResult` strips both branded results, so neither reaches `content`.
  //
  // Each brand takes a status that admits it: the unparsed one states that the call
  // completed and this build could not read its payload, and a failure states that
  // the call did not complete at all.
  it('answers nothing for a result this build could not read', () => {
    expect(resultImages(toolCallFixture('mcp', { result: unparsedResult('raw bytes') }))).toEqual([])
    expect(resultImages(toolCallFixture('mcp', { status: 'failed', result: failedResult('the server refused') }))).toEqual([])
  })

  it('answers nothing for a generic result whose content holds no picture', () => {
    expect(resultImages(toolCallFixture('other', { result: { content: [{ type: 'text', text: 'words' }] } }))).toEqual([])
  })
})

describe('isGenericCall', () => {
  it.each(['unspecified', 'other', 'mcp'] as const)('answers true for a %s call', (kind) => {
    expect(isGenericCall(toolCallFixture(kind))).toBe(true)
  })

  it.each(['read', 'execute', 'grep', 'todo'] as const)('answers false for a %s call', (kind) => {
    expect(isGenericCall(toolCallFixture(kind))).toBe(false)
  })
})
