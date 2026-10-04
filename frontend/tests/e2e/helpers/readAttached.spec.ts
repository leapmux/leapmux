import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from '../fixtures'
import { createTestDirectory } from './runDirectory'
import { writeToolImage } from './toolImages'
import { readAttached, readAttachedWithArgument } from './ui'

test.describe('readAttached', () => {
  for (const argument of [0, false, '']) {
    test(`retains the explicit argument ${JSON.stringify(argument)} after a chat row replacement`, async ({ page }) => {
      await page.setContent('<main><div data-testid="message-bubble" data-message-seq="1">First row</div></main>')
      const bubbles = page.locator('[data-testid="message-bubble"]:visible')
      const result = await readAttachedWithArgument(bubbles, 'the replaced row with an argument', async (matches, value) => {
        const row = matches.find(element => element.isConnected)
        if (!row)
          return null
        if (row.getAttribute('data-message-seq') === '1') {
          const replacement = row.cloneNode(true)
          if (!(replacement instanceof HTMLElement))
            throw new Error('The replacement chat row must be an HTML element.')
          replacement.setAttribute('data-message-seq', '2')
          row.replaceWith(replacement)
          return null
        }
        return { sequence: row.getAttribute('data-message-seq'), argument: value }
      }, argument)
      expect(result).toEqual({ sequence: '2', argument })
    })
  }

  test('retries a chat row that detaches during its read', async ({ page }) => {
    await page.setContent('<main data-chat-scroll-container="true"><div data-testid="message-bubble" data-message-seq="1">First row</div></main>')
    const bubbles = page.locator('[data-testid="message-bubble"]:visible')

    const sequence = await readAttached(bubbles, 'the replaced chat row', (matches) => {
      const row = matches.find(element => element.isConnected)
      if (!row)
        return null
      if (row.getAttribute('data-message-seq') === '1') {
        const replacement = row.ownerDocument.createElement('div')
        replacement.setAttribute('data-testid', 'message-bubble')
        replacement.setAttribute('data-message-seq', '2')
        replacement.textContent = 'Replacement row'
        row.replaceWith(replacement)
        return null
      }
      return row.getAttribute('data-message-seq')
    })

    expect(sequence).toBe('2')
    await expect(bubbles).toHaveAttribute('data-message-seq', '2')
  })

  test('decodes an image after its chat bubble is replaced', async ({ page }) => {
    const directory = createTestDirectory('attached-image-')
    const name = writeToolImage(directory, 'replacement')
    const data = readFileSync(join(directory, name)).toString('base64')
    await page.setContent(`<main data-chat-scroll-container="true"><div data-testid="message-bubble"><img alt="Tool picture" width="10" height="10" src="data:image/png;base64,${data}"></div></main>`)
    const image = page.locator('[data-testid="message-bubble"]:visible img:visible')

    await expect.poll(() => readAttached(image, 'the replaced tool image', (matches) => {
      const attached = matches.find((element): element is HTMLImageElement => element.isConnected && element instanceof HTMLImageElement)
      if (!attached)
        return null
      const bubble = attached.closest('[data-testid="message-bubble"]')
      if (!bubble)
        return null
      if (!bubble.hasAttribute('data-replacement')) {
        const replacement = bubble.ownerDocument.createElement('div')
        replacement.setAttribute('data-testid', 'message-bubble')
        replacement.setAttribute('data-replacement', '')
        const nextImage = bubble.ownerDocument.createElement('img')
        nextImage.src = attached.src
        nextImage.alt = attached.alt
        nextImage.width = 10
        nextImage.height = 10
        replacement.append(nextImage)
        bubble.replaceWith(replacement)
        return null
      }
      return attached.naturalWidth
    })).toBeGreaterThan(0)
  })
})
