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

    expect(screen.getByTestId('item-a')).toBeInTheDocument()
    expect(screen.getByTestId('item-b')).toBeInTheDocument()
    expect(screen.getByTestId('item-c')).toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('renders all items when count equals maxVisible', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c']}
        maxVisible={3}
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    expect(screen.getByTestId('item-a')).toBeInTheDocument()
    expect(screen.getByTestId('item-b')).toBeInTheDocument()
    expect(screen.getByTestId('item-c')).toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('collapses items exceeding maxVisible and shows toggle', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c', 'd', 'e']}
        maxVisible={2}
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    expect(screen.getByTestId('item-a')).toBeInTheDocument()
    expect(screen.getByTestId('item-b')).toBeInTheDocument()
    expect(screen.queryByTestId('item-c')).not.toBeInTheDocument()
    expect(screen.queryByTestId('item-d')).not.toBeInTheDocument()
    expect(screen.queryByTestId('item-e')).not.toBeInTheDocument()

    const toggle = screen.getByRole('button')
    expect(toggle).toHaveTextContent('Show 3 more\u2026')
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

    expect(screen.getByTestId('item-a')).toBeInTheDocument()
    expect(screen.getByTestId('item-b')).toBeInTheDocument()
    expect(screen.getByTestId('item-c')).toBeInTheDocument()
    expect(screen.getByTestId('item-d')).toBeInTheDocument()
    expect(screen.getByTestId('item-e')).toBeInTheDocument()

    expect(screen.getByRole('button')).toHaveTextContent('Show less')
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

    expect(screen.queryByTestId('item-c')).not.toBeInTheDocument()
    expect(toggle).toHaveTextContent('Show 3 more\u2026')
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
    expect(toggle).toHaveTextContent('2 hidden items')

    fireEvent.click(toggle)
    expect(toggle).toHaveTextContent('Collapse')
  })

  it('handles singular "more" label for 1 hidden item', () => {
    render(() => (
      <CollapsibleList
        items={['a', 'b', 'c']}
        maxVisible={2}
        renderItem={item => <span data-testid={`item-${item}`}>{item}</span>}
      />
    ))

    expect(screen.getByRole('button')).toHaveTextContent('Show 1 more\u2026')
  })
})
