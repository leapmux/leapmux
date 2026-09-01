/// <reference types="vitest/globals" />
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { LabeledField } from '~/components/common/LabeledField'

describe('labeledField', () => {
  it('draws the label, the actions and the control in one frame', () => {
    render(() => (
      <LabeledField label="Shell" actions={<button type="button">Refresh</button>}>
        <input aria-label="Shell" />
      </LabeledField>
    ))

    expect(screen.getByText('Shell')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeInTheDocument()
    expect(screen.getByLabelText('Shell')).toBeInTheDocument()
  })

  // The rule this wrapper exists to hold. A real `<label>` takes Oat's heavier
  // type rule and typesets one field differently from the one beside it, which
  // is the defect that removing the Shell field's `<label>` fixed.
  it('renders the label as a plain div, never a label element', () => {
    const { container } = render(() => (
      <LabeledField label="Worker"><input aria-label="Worker" /></LabeledField>
    ))
    expect(container.querySelector('label')).toBeNull()
  })

  it('takes fields with no actions and no error', () => {
    const { container } = render(() => (
      <LabeledField label="Base Branch"><input aria-label="Base Branch" /></LabeledField>
    ))
    expect(screen.getByText('Base Branch')).toBeInTheDocument()
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })

  it('takes more than one action', () => {
    render(() => (
      <LabeledField
        label="Working Directory"
        actions={(
          <>
            <button type="button">Hidden</button>
            <button type="button">Refresh</button>
          </>
        )}
      >
        <div>tree</div>
      </LabeledField>
    ))
    expect(screen.getByRole('button', { name: 'Hidden' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeInTheDocument()
  })

  // The error is announced and carries the id the CONTROL points at, because a
  // field whose error also disables submit otherwise leaves a user who cannot
  // see the red text with a dead button and no reason.
  it('announces the error and gives it the id the control points at', () => {
    render(() => (
      <LabeledField label="Title" error="Name must not be empty" errorId="title-err">
        <input aria-label="Title" aria-describedby="title-err" />
      </LabeledField>
    ))

    const message = screen.getByRole('alert')
    expect(message).toHaveTextContent('Name must not be empty')
    expect(message).toHaveAttribute('id', 'title-err')
    expect(screen.getByLabelText('Title')).toHaveAttribute('aria-describedby', 'title-err')
  })

  it('draws no error node while the field is acceptable', () => {
    const { container } = render(() => (
      <LabeledField label="Title" error={null} errorId="title-err">
        <input aria-label="Title" />
      </LabeledField>
    ))
    expect(container.querySelector('#title-err')).toBeNull()
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })

  it('puts the caller class on the outer element, for a field that needs its own layout', () => {
    const { container } = render(() => (
      <LabeledField class="vstack gap-1" label="Working Directory"><div>tree</div></LabeledField>
    ))
    expect(container.firstElementChild).toHaveClass('vstack', 'gap-1')
  })
})
