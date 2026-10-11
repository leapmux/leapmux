import { afterEach, describe, expect, it } from 'vitest'
import { rowLayoutMeasurement } from './rowLayoutMeasurement'

const rows: HTMLElement[] = []
function row(height: number, width = 400): HTMLElement {
  const element = document.createElement('div')
  element.getBoundingClientRect = () => new DOMRect(0, 0, width, height)
  document.body.append(element)
  rows.push(element)
  return element
}
afterEach(() => {
  for (const element of rows.splice(0))
    element.remove()
})

describe('rowLayoutMeasurement', () => {
  it('accepts an empty mounted row with layout width', () => {
    expect(rowLayoutMeasurement(row(0))).toBe(0)
  })

  it('accepts positive heights and very large heights', () => {
    expect(rowLayoutMeasurement(row(0.2))).toBe(0.2)
    expect(rowLayoutMeasurement(row(1000000))).toBe(1000000)
  })

  it('accepts an empty row with hidden visibility because it keeps layout', () => {
    const element = row(0)
    element.style.visibility = 'hidden'
    expect(rowLayoutMeasurement(element)).toBe(0)
  })

  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY])('rejects zero height without a usable width of %s', (width) => {
    expect(rowLayoutMeasurement(row(0, width))).toBeUndefined()
  })

  it('rejects a view that has no layout until the view becomes visible', () => {
    const element = row(0, 0)
    element.style.display = 'none'
    expect(rowLayoutMeasurement(element)).toBeUndefined()
    element.style.display = ''
    element.getBoundingClientRect = () => new DOMRect(0, 0, 400, 0)
    expect(rowLayoutMeasurement(element)).toBe(0)
  })

  it('rejects detached rows and an absent element', () => {
    const element = row(0)
    element.remove()
    expect(rowLayoutMeasurement(element)).toBeUndefined()
    const positive = row(40)
    positive.remove()
    expect(rowLayoutMeasurement(positive)).toBeUndefined()
    expect(rowLayoutMeasurement(undefined)).toBeUndefined()
  })

  it.each([-1, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY])('rejects an invalid height of %s', (height) => {
    expect(rowLayoutMeasurement(row(height))).toBeUndefined()
  })

  it('waits for every incomplete image before accepting zero', () => {
    const element = row(0)
    const image = document.createElement('img')
    let complete = false
    Object.defineProperty(image, 'complete', { get: () => complete })
    element.append(image)
    expect(rowLayoutMeasurement(element)).toBeUndefined()
    complete = true
    expect(rowLayoutMeasurement(element)).toBe(0)
  })

  it('preserves a positive reserved image box before decoding finishes', () => {
    const element = row(24)
    const image = document.createElement('img')
    Object.defineProperty(image, 'complete', { get: () => false })
    element.append(image)
    expect(rowLayoutMeasurement(element)).toBe(24)
  })
})
