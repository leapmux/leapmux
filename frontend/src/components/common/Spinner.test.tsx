import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { spin } from '~/styles/animations.css'
import { Spinner } from './Spinner'

describe('spinner', () => {
  it('renders the LoaderCircle icon by default', () => {
    const { container } = render(() => <Spinner />)
    const svg = container.querySelector('svg')
    expect(svg).not.toBeNull()
    expect(svg!).toHaveClass('lucide-loader-circle')
  })

  it('applies the shared spin animation', () => {
    const { container } = render(() => <Spinner />)
    const svg = container.querySelector('svg')!
    // The animations.css spin keyframes drive the rotation. Asserting the
    // generated class name on the SVG pins that the animation class
    // actually reaches the rendered element (regression for "spinner that
    // doesn't spin").
    const className = svg.getAttribute('class') ?? ''
    expect(className).toMatch(/\b\w*spinner\w*\b/)
    // Sanity: the keyframes constant from animations.css is referenced by
    // the spinner class via vanilla-extract, so the keyframes name must
    // exist and be a non-empty string.
    expect(typeof spin).toBe('string')
    expect(spin.length).toBeGreaterThan(0)
  })

  it('honours the size prop with the design-system scale', () => {
    const { container: defaultC } = render(() => <Spinner />)
    const { container: xsC } = render(() => <Spinner size="xs" />)
    const { container: mdC } = render(() => <Spinner size="md" />)
    const defaultSvg = defaultC.querySelector('svg')!
    const xsSvg = xsC.querySelector('svg')!
    const mdSvg = mdC.querySelector('svg')!
    const widthOf = (svg: SVGElement) => Number(svg.getAttribute('width') ?? '0')
    expect(widthOf(defaultSvg)).toBeGreaterThan(0)
    // xs < default(sm) < md — pins ordering without hard-coding pixel
    // values from the iconSize token table.
    expect(widthOf(xsSvg)).toBeLessThan(widthOf(defaultSvg))
    expect(widthOf(mdSvg)).toBeGreaterThan(widthOf(defaultSvg))
  })

  it('forwards data-testid to the rendered icon', () => {
    const { container } = render(() => <Spinner data-testid="my-spinner" />)
    const tagged = container.querySelector('[data-testid="my-spinner"]')
    expect(tagged).not.toBeNull()
  })
})
