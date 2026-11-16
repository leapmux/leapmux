import { describe, expect, it, vi } from 'vitest'
import { NO_FILE_IMAGES } from '~/components/chat/fileImageResolver'
import { testImageActions } from './imageActions'

describe('testImageActions', () => {
  it('defers no read, and has no source for an image file, by default', async () => {
    const actions = testImageActions()

    expect(actions.deferLoad()).toBe(false)
    expect(actions.premeasurePass()).toBe(false)
    expect(actions.files.peekSize('/image.png')).toEqual(NO_FILE_IMAGES.peekSize('/image.png'))
    await expect(actions.files.load('/image.png')).rejects.toThrow('The image source is unavailable')
  })

  it('keeps each member that a case sets, and the defaults of the other image file members', async () => {
    const image = { data: 'AAAA', mimeType: 'image/png' }
    const open = vi.fn()
    const actions = testImageActions({
      files: {
        load: async () => image,
      },
      openImage: open,
      deferLoad: () => true,
    })

    await expect(actions.files.load('/image.png')).resolves.toBe(image)
    expect(actions.files.peek('/image.png')).toBeUndefined()
    await expect(actions.files.loadSize('/image.png')).resolves.toBeUndefined()
    expect(actions.deferLoad()).toBe(true)
    actions.openImage?.({ index: 0 })
    expect(open).toHaveBeenCalledExactlyOnceWith({ index: 0 })
  })
})
