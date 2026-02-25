import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { CollapsibleList } from '~/components/chat/controls/CollapsibleList'

describe('collapsibleList', () => {
  it('renders all items when count is within maxVisible', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c']}
        maxVisible={5}
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    expect(screen.getByTestId('item-a')).toBeTruthy()
    expect(screen.getByTestId('item-b')).toBeTruthy()
    expect(screen.getByTestId('item-c')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('renders all items when count equals maxVisible', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c']}
        maxVisible={3}
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    expect(screen.getByTestId('item-a')).toBeTruthy()
    expect(screen.getByTestId('item-b')).toBeTruthy()
    expect(screen.getByTestId('item-c')).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('collapses items exceeding maxVisible and shows toggle', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c', 'd', 'e']}
        maxVisible={2}
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    expect(screen.getByTestId('item-a')).toBeTruthy()
    expect(screen.getByTestId('item-b')).toBeTruthy()
    expect(screen.queryByTestId('item-c')).toBeNull()
    expect(screen.queryByTestId('item-d')).toBeNull()
    expect(screen.queryByTestId('item-e')).toBeNull()

    const toggle = screen.getByRole('button')
    expect(toggle.textContent).toBe('Show 3 more\u2026')
  })

  it('expands all items when toggle is clicked', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c', 'd', 'e']}
        maxVisible={2}
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    fireEvent.click(screen.getByRole('button'))

    expect(screen.getByTestId('item-a')).toBeTruthy()
    expect(screen.getByTestId('item-b')).toBeTruthy()
    expect(screen.getByTestId('item-c')).toBeTruthy()
    expect(screen.getByTestId('item-d')).toBeTruthy()
    expect(screen.getByTestId('item-e')).toBeTruthy()

    expect(screen.getByRole('button').textContent).toBe('Show less')
  })

  it('collapses back when toggle is clicked twice', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c', 'd', 'e']}
        maxVisible={2}
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    const toggle = screen.getByRole('button')
    fireEvent.click(toggle) // expand
    fireEvent.click(toggle) // collapse

    expect(screen.queryByTestId('item-c')).toBeNull()
    expect(toggle.textContent).toBe('Show 3 more\u2026')
  })

  it('uses custom moreLabel and lessLabel', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c']}
        maxVisible={1}
        moreLabel={n => `${n} hidden items`}
        lessLabel="Collapse"
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    const toggle = screen.getByRole('button')
    expect(toggle.textContent).toBe('2 hidden items')

    fireEvent.click(toggle)
    expect(toggle.textContent).toBe('Collapse')
  })

  it('handles singular "more" label for 1 hidden item', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c']}
        maxVisible={2}
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    expect(screen.getByRole('button').textContent).toBe('Show 1 more\u2026')
  })
})
