import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { SvgIconFrame } from './SvgIconFrame'

// The `data-*` index signature is what a JSX attribute list allows and a plain
// object literal does not, and the frame has to forward those: `WorkingTreeIcon`
// tells its two glyphs apart with a `data-testid` that reaches the svg.
type FrameProps = Omit<Parameters<typeof SvgIconFrame>[0], 'children'> & { [key: `data-${string}`]: string }

function renderFrame(props: FrameProps = {}) {
  const { container } = render(() => (
    <SvgIconFrame {...props}>
      <path d="M4 4h16" />
    </SvgIconFrame>
  ))
  return container.querySelector('svg')!
}

describe('svgIconFrame (SvgIconFrame)', () => {
  it('draws the caller paths inside a 24x24 lucide-shaped frame', () => {
    const svg = renderFrame()

    expect(svg.getAttribute('viewBox')).toBe('0 0 24 24')
    expect(svg.getAttribute('stroke')).toBe('currentColor')
    expect(svg.getAttribute('stroke-width')).toBe('2')
    expect(svg.getAttribute('stroke-linecap')).toBe('round')
    expect(svg.getAttribute('fill')).toBe('none')
    expect(svg.querySelector('path')!.getAttribute('d')).toBe('M4 4h16')
  })

  it('sizes both axes from one size prop, and defaults to 24', () => {
    expect(renderFrame({ size: 14 }).getAttribute('width')).toBe('14')
    expect(renderFrame({ size: 14 }).getAttribute('height')).toBe('14')
    expect(renderFrame().getAttribute('width')).toBe('24')
  })

  it('takes the stroke colour and the stroke width from the caller', () => {
    const svg = renderFrame({ color: 'red', strokeWidth: 1.5, class: 'my-icon' })

    expect(svg.getAttribute('stroke')).toBe('red')
    expect(svg.getAttribute('stroke-width')).toBe('1.5')
    expect(svg.getAttribute('class')).toBe('my-icon')
  })

  it('forwards an unrecognised prop to the svg', () => {
    expect(renderFrame({ 'data-testid': 'my-glyph' }).getAttribute('data-testid')).toBe('my-glyph')
  })

  // The default: a glyph inside a button that a tooltip already names is
  // decoration, and a second nameless node in the accessibility tree is noise.
  it('hides a glyph that carries no role and no accessible name', () => {
    expect(renderFrame().getAttribute('aria-hidden')).toBe('true')
  })

  it('stays visible to a screen reader once the caller gives it a role or a name', () => {
    expect(renderFrame({ 'role': 'img', 'aria-label': 'Worktree' }).getAttribute('aria-hidden')).toBeNull()
    expect(renderFrame({ 'aria-label': 'Worktree' }).getAttribute('aria-hidden')).toBeNull()
    expect(renderFrame({ 'aria-labelledby': 'x' }).getAttribute('aria-hidden')).toBeNull()
  })

  // The rule lucide-solid applies, copied here so a hand-drawn glyph and a
  // lucide glyph that swap places in one `icon` prop behave alike: the KEY
  // decides, not its value. A call site that wants no role must pass no key.
  it('treats a role key with an undefined value as present', () => {
    expect(renderFrame({ role: undefined }).getAttribute('aria-hidden')).toBeNull()
  })

  it('lets the caller override the hidden default', () => {
    expect(renderFrame({ 'aria-hidden': 'false' }).getAttribute('aria-hidden')).toBe('false')
  })
})
