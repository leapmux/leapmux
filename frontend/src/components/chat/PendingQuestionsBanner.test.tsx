import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PendingQuestionsBanner } from './PendingQuestionsBanner'

describe('PendingQuestionsBanner', () => {
  it('states one waiting question in the singular', () => {
    render(() => <PendingQuestionsBanner count={1} shown onAnswer={vi.fn()} />)
    // getByText resolves ONE element, so this also proves the sentence exists
    // in one node only.
    expect(screen.getByText('The agent asked a question.')).toBeInTheDocument()
    expect(screen.getByTestId('pending-questions-banner')).toHaveAttribute('role', 'status')
    expect(screen.getByTestId('pending-questions-banner')).toHaveAttribute('aria-live', 'polite')
  })

  it('counts more than one waiting question', () => {
    render(() => <PendingQuestionsBanner count={3} shown onAnswer={vi.fn()} />)
    expect(screen.getByText('The agent asked 3 questions.')).toBeInTheDocument()
  })

  it('keeps the live region mounted but hidden while no question shows', () => {
    const { container } = render(() => <PendingQuestionsBanner count={1} shown={false} onAnswer={vi.fn()} />)
    // The node stays mounted so a later question announces. Its class swaps to
    // the hidden style, and it carries no test id while hidden.
    const live = container.querySelector('[role="status"]')
    expect(live).not.toBeNull()
    expect(live?.className).toContain('srOnly')
    expect(live).not.toHaveAttribute('data-testid')
    expect(live).toHaveTextContent('')
  })

  it('calls onAnswer from the Answer button', async () => {
    const onAnswer = vi.fn()
    render(() => <PendingQuestionsBanner count={2} shown onAnswer={onAnswer} />)
    await fireEvent.click(screen.getByTestId('pending-questions-answer'))
    expect(onAnswer).toHaveBeenCalledOnce()
  })
})
