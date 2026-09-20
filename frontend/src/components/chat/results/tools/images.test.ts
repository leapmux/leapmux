import type { ImageResultSource } from '~/lib/imageBlocks'
import { describe, expect, it } from 'vitest'
import { imagesForRow } from '~/components/chat/results/rowImages'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'

const picture = (name: string): ImageResultSource => ({ mimeType: 'image/png', data: name })

describe('imagesForRow over merged calls', () => {
  it('numbers a read call\'s own pictures from zero', () => {
    const call = toolCallFixture('read', { images: [picture('a'), picture('b')] })
    expect(imagesForRow(toolRow(call)).map(p => p.data)).toEqual(['a', 'b'])
  })

  it('numbers a generic result\'s inline pictures from zero', () => {
    const call = toolCallFixture('mcp', { result: { content: [
      { type: 'text', text: 'before' },
      { type: 'image', source: picture('one') },
      { type: 'image', source: picture('two') },
    ] } })
    expect(imagesForRow(toolRow(call)).map(p => p.data)).toEqual(['one', 'two'])
  })

  it('numbers extra content first, then the call\'s own pictures', () => {
    const call = toolCallFixture('read', {
      images: [picture('own')],
      extraContent: [{ type: 'image', source: picture('extra') }],
    })
    expect(imagesForRow(toolRow(call)).map(p => p.data)).toEqual(['extra', 'own'])
  })

  it('lists nothing on a request row whose result row is beside it', () => {
    const call = toolCallFixture('read', { images: [picture('a')] })
    const row = toolRow(call, 'request', { result: true })
    expect(imagesForRow(row)).toEqual([])
  })

  // A call carries pictures only once it answered, so a lone request row draws the
  // finished side of its span rather than a call still in flight.
  it('lists the pictures of a request row that stands alone', () => {
    const call = toolCallFixture('read', { images: [picture('a')] })
    const row = toolRow(call, 'request', { result: false })
    expect(imagesForRow(row).map(p => p.data)).toEqual(['a'])
  })
})
