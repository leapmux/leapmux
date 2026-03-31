import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { CollapsibleText } from '~/components/chat/controls/CollapsibleText'

/** Get the text content of the rendered <pre> element. */
function getPreText(): string {
  return document.querySelector('pre')?.textContent ?? ''
}

describe('collapsibleText', () => {
  it('renders full text when lines are within maxLines', () => {
    const text = 'line 1\nline 2\nline 3'
    render(() => <CollapsibleText text={text} maxLines={5} />)

    expect(getPreText()).toBe(text)
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('renders full text when lines equal maxLines', () => {
    const text = 'line 1\nline 2\nline 3'
    render(() => <CollapsibleText text={text} maxLines={3} />)

    expect(getPreText()).toBe(text)
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('truncates text exceeding maxLines and shows toggle', () => {
    render(() => <CollapsibleText text={'line 1\nline 2\nline 3\nline 4\nline 5'} maxLines={2} />)

    expect(getPreText()).toBe('line 1\nline 2')

    const toggle = screen.getByRole('button')
    expect(toggle).toHaveTextContent('Show 3 more lines\u2026')
  })

  it('expands to full text when toggle is clicked', () => {
    const text = 'line 1\nline 2\nline 3\nline 4\nline 5'
    render(() => <CollapsibleText text={text} maxLines={2} />)

    fireEvent.click(screen.getByRole('button'))

    expect(getPreText()).toBe(text)
    expect(screen.getByRole('button')).toHaveTextContent('Show less')
  })

  it('collapses back when toggle is clicked twice', () => {
    render(() => <CollapsibleText text={'line 1\nline 2\nline 3\nline 4\nline 5'} maxLines={2} />)

    const toggle = screen.getByRole('button')
    fireEvent.click(toggle) // expand
    fireEvent.click(toggle) // collapse

    expect(getPreText()).toBe('line 1\nline 2')
    expect(toggle).toHaveTextContent('Show 3 more lines\u2026')
  })

  it('renders as pre tag by default', () => {
    render(() => <CollapsibleText text="hello" maxLines={5} />)
    expect(document.querySelector('pre')).toBeInTheDocument()
    expect(document.querySelector('pre')).toHaveTextContent('hello')
  })

  it('renders as div tag when specified', () => {
    const { container } = render(() => <CollapsibleText text="hello" maxLines={5} tag="div" />)
    // The render wrapper is a <div>, the CollapsibleText also renders a <div>.
    // Find the inner div (child of the container).
    const innerDiv = container.querySelector('div')
    expect(innerDiv).toBeInTheDocument()
    expect(innerDiv).toHaveTextContent('hello')
  })

  it('applies custom class', () => {
    render(() => <CollapsibleText text="hello" maxLines={5} class="my-class" />)
    expect(document.querySelector('pre.my-class')).toBeInTheDocument()
  })

  it('handles singular line label', () => {
    render(() => <CollapsibleText text={'line 1\nline 2\nline 3'} maxLines={2} />)
    expect(screen.getByRole('button')).toHaveTextContent('Show 1 more line\u2026')
  })
})
