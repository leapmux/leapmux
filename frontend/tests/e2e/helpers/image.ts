import type { Page } from '@playwright/test'
import { Buffer } from 'node:buffer'

/** Create a decodable PNG. Noise keeps larger fixtures above the text preview limit. */
export async function createImageBytes(page: Page, width: number, height: number): Promise<Buffer> {
  const data = await page.evaluate(({ width, height }) => {
    const canvas = document.createElement('canvas')
    canvas.width = width
    canvas.height = height
    const context = canvas.getContext('2d')!
    const image = context.createImageData(width, height)
    let value = 1729
    for (let index = 0; index < image.data.length; index++) {
      value = (Math.imul(value, 1664525) + 1013904223) | 0
      image.data[index] = index % 4 === 3 ? 255 : value >>> 24
    }
    context.putImageData(image, 0, 0)
    return canvas.toDataURL('image/png').slice('data:image/png;base64,'.length)
  }, { width, height })
  return Buffer.from(data, 'base64')
}
