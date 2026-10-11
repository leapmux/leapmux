import { describe, expect, it } from 'vitest'
import { zcodeUserImageUrls } from './nodeImageScenario'

describe('zcodeUserImageUrls', () => {
  const image = (url: unknown) => ({ type: 'image_url', image_url: { url } })

  it('reads each image URL of each user message, in order', () => {
    const body = {
      messages: [
        { role: 'user', content: [{ type: 'text', text: 'Show it.' }, image('data:image/png;base64,AAAA')] },
        { role: 'tool', tool_call_id: 'call', content: 'done' },
        { role: 'user', content: [image('data:image/png;base64,BBBB')] },
      ],
    }
    expect(zcodeUserImageUrls({ body })).toEqual(['data:image/png;base64,AAAA', 'data:image/png;base64,BBBB'])
  })

  it('reads no image of another role, of a text content, or without a URL string', () => {
    const body = {
      messages: [
        { role: 'assistant', content: [image('data:image/png;base64,AAAA')] },
        { role: 'tool', content: [image('data:image/png;base64,BBBB')] },
        { role: 'user', content: 'data:image/png;base64,CCCC' },
        { role: 'user', content: [image(42), { type: 'image_url' }, null] },
      ],
    }
    expect(zcodeUserImageUrls({ body })).toEqual([])
  })

  it.each([null, 'text', {}, { messages: 'none' }])('reads no image from a body without a message list: %j', (body) => {
    expect(zcodeUserImageUrls({ body })).toEqual([])
  })
})
