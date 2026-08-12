import { render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { clippedText } from '~/styles/shared.css'
import { hoverForTooltip, stubClipped, stubFitting } from '~/test-support/clipStub'
import { ClippedText } from './ClippedText'

function label(container: HTMLElement): HTMLElement {
  const el = container.querySelector('span[class]')
  expect(el).toBeTruthy()
  return el as HTMLElement
}

/** The class tokens on the element, so a test asserts membership, not a substring. */
function classes(el: Element): string[] {
  return el.className.trim().split(/\s+/)
}

describe('clippedText', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('renders the text in a single span carrying the clipping style', () => {
    const { container } = render(() => <ClippedText text="a worker name" />)

    // Tooltip resolves its target as the ONE direct element child of its
    // display:contents wrapper, so a second element here would disable it.
    const wrapper = container.firstElementChild!
    expect(wrapper.childElementCount).toBe(1)
    const el = label(container)
    expect(el.tagName).toBe('SPAN')
    expect(el.textContent).toBe('a worker name')
    expect(classes(el)).toContain(clippedText)
  })

  // Tooltip's wrapper must stay transparent to layout. Every clipped label is a
  // flex item that needs `min-width: 0` to shrink, which only holds while the
  // wrapper does not become a box of its own between the row and the label.
  it('leaves the tooltip wrapper transparent to layout', () => {
    const { container } = render(() => <ClippedText text="x" />)

    expect((container.firstElementChild as HTMLElement).style.display).toBe('contents')
  })

  it('merges an extra class after the clipping style', () => {
    const { container } = render(() => <ClippedText text="x" class="taskSecondary" />)

    const el = label(container)
    expect(classes(el)).toContain(clippedText)
    expect(classes(el)).toContain('taskSecondary')
  })

  it('applies testId to the span itself, not to the wrapper', () => {
    const { container } = render(() => <ClippedText text="w1" testId="worker-name" />)

    const el = screen.getByTestId('worker-name')
    expect(el).toBe(label(container))
  })

  it('suppresses the tooltip while the label fits', () => {
    const { container } = render(() => <ClippedText text="short" />)

    const el = label(container)
    stubFitting(el)

    expect(hoverForTooltip(el)).toBeNull()
    expect(el).not.toHaveAttribute('aria-describedby')
  })

  it('shows the full text once the label is clipped', () => {
    const { container } = render(() => (
      <ClippedText text="a label far wider than the row that holds it" />
    ))

    const el = label(container)
    stubClipped(el)

    expect(hoverForTooltip(el)?.textContent).toBe('a label far wider than the row that holds it')
  })

  // The point of `detail`: the explanation is ADDED to the label, never put in
  // its place. A caller that supplies an explanation must not thereby spend the
  // clipped label's own route back.
  it('shows the label above its detail, not in place of it', () => {
    const { container } = render(() => (
      <ClippedText text="Interrupted" detail="stopped by a worker restart" />
    ))

    const el = label(container)
    stubClipped(el)

    const tip = hoverForTooltip(el)!
    expect(tip.textContent).toContain('Interrupted')
    expect(tip.textContent).toContain('stopped by a worker restart')
  })

  // A detail carries what the label CANNOT, so clipping is not its trigger.
  it('shows a detail while the label fits', () => {
    const { container } = render(() => (
      <ClippedText text="Write the docs" detail="Describe every flag" />
    ))

    const el = label(container)
    stubFitting(el)

    expect(hoverForTooltip(el)?.textContent).toContain('Describe every flag')
  })

  // An EMPTY detail is an absent one. It must not force the tooltip open on a
  // label that fits, and it must not render a blank second line.
  it('treats an empty detail as absent', () => {
    const { container } = render(() => <ClippedText text="the full label" detail="" />)

    const el = label(container)
    stubFitting(el)
    expect(hoverForTooltip(el)).toBeNull()

    stubClipped(el)
    expect(hoverForTooltip(el)?.textContent).toBe('the full label')
  })

  it('obeys an explicit showWhen over the default that detail sets', () => {
    const { container } = render(() => (
      <ClippedText text="Task" detail="why it matters" showWhen="clipped" />
    ))

    const el = label(container)
    stubFitting(el)

    expect(hoverForTooltip(el)).toBeNull()
  })

  it('renders no tooltip for an empty label', () => {
    const { container } = render(() => <ClippedText text="" />)

    const el = label(container)
    stubClipped(el)

    expect(hoverForTooltip(el)).toBeNull()
  })

  it('tracks a changing text in both the label and its tooltip', () => {
    const [text, setText] = createSignal('first')
    const { container } = render(() => <ClippedText text={text()} />)

    const el = label(container)
    expect(el.textContent).toBe('first')

    setText('second')
    expect(el.textContent).toBe('second')

    stubClipped(el)
    expect(hoverForTooltip(el)?.textContent).toBe('second')
  })

  // A detail that arrives after the first render must start to show the
  // tooltip, so the prop has to stay inside a tracked scope.
  it('tracks a detail that appears later', () => {
    const [detail, setDetail] = createSignal<string | undefined>()
    const { container } = render(() => <ClippedText text="Run tests" detail={detail()} />)

    const el = label(container)
    stubFitting(el)
    expect(hoverForTooltip(el)).toBeNull()

    setDetail('the whole suite')
    expect(hoverForTooltip(el)?.textContent).toContain('the whole suite')
  })
})
