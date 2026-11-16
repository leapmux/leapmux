import type { ImageResultSource } from '~/lib/imageBlocks'
import { describe, expect, it } from 'vitest'
import { imagesForRow } from '~/components/chat/results/rowImages'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'

const picture = (name: string): ImageResultSource => ({ mimeType: 'image/png', data: name })

describe('imagesForRow over merged calls', () => {
  it('numbers a read call\'s own pictures from zero', () => {
    const call = toolCallFixture('read', {
      images: [
        picture('a'),
        picture('b'),
      ],
    })
    expect(imagesForRow(toolRow(call)).map(p => p.data)).toEqual(['a', 'b'])
  })

  it('numbers a generic result\'s inline pictures from zero', () => {
    const call = toolCallFixture('mcp', {
      result: {
        content: [
          { type: 'text', text: 'before' },
          {
            type: 'image',
            source: picture('one'),
          },
          {
            type: 'image',
            source: picture('two'),
          },
        ],
      },
    })
    expect(imagesForRow(toolRow(call)).map(p => p.data)).toEqual(['one', 'two'])
  })

  it('numbers extra content first, then the call\'s own pictures', () => {
    const call = toolCallFixture('read', {
      images: [
        picture('own'),
      ],
      extraContent: [
        {
          type: 'image',
          source: picture('extra'),
        },
      ],
    })
    expect(imagesForRow(toolRow(call)).map(p => p.data)).toEqual(['extra', 'own'])
  })

  it('lists nothing on a request row whose result row is beside it', () => {
    const call = toolCallFixture('read', {
      images: [
        picture('a'),
      ],
    })
    const row = toolRow(call, 'request')
    expect(imagesForRow(row)).toEqual([])
  })

  // A call carries pictures only once it answered, and only the result row draws the
  // answer: a request row never reads its result.
  it('lists no pictures on a request row', () => {
    const call = toolCallFixture('read', {
      images: [
        picture('a'),
      ],
    })
    expect(imagesForRow(toolRow(call, 'request'))).toEqual([])
    expect(imagesForRow(toolRow(call, 'result')).map(p => p.data)).toEqual(['a'])
  })
})
