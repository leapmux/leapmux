/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { TitleInput } from '~/components/shell/TitleInput'
import { createTitleState } from '~/hooks/createTitleState'

/** Renders the field over a real TitleState, disposed after the test. */
function renderTitleInput(generate: () => string = () => 'Agent Gabe') {
  const dispose = createRoot((d) => {
    const state = createTitleState(generate)
    render(() => <TitleInput state={state} />)
    return d
  })
  return { dispose }
}

describe('titleInput', () => {
  it('renders the generated title in the input', () => {
    renderTitleInput()
    expect(screen.getByTestId('title-input')).toHaveValue('Agent Gabe')
  })

  // The label is a plain div, so the input would otherwise have no accessible
  // name at all -- a screen reader would announce an unlabelled text field.
  it('gives the input an accessible name', () => {
    renderTitleInput()
    expect(screen.getByRole('textbox', { name: 'Title' })).toBeInTheDocument()
  })

  // A HINT, not a sample value. The field is empty only while submit is
  // disabled, so a placeholder that reads like an acceptable title offered one
  // the dialog refuses.
  it('shows what to do rather than a title it would refuse', () => {
    renderTitleInput()
    const placeholder = screen.getByTestId('title-input').getAttribute('placeholder')
    expect(placeholder).toBe('Type a name')
    // The old placeholders were values, and the gate refuses none of them --
    // which is what made them misleading when the field was empty.
    expect(placeholder).not.toMatch(/^New /)
  })

  it('writes what the user types back into the field', () => {
    renderTitleInput()
    fireEvent.input(screen.getByTestId('title-input'), { target: { value: 'my own name' } })
    expect(screen.getByTestId('title-input')).toHaveValue('my own name')
  })

  it('re-rolls the title from the refresh button', () => {
    let n = 0
    renderTitleInput(() => `Agent ${++n}`)
    expect(screen.getByTestId('title-input')).toHaveValue('Agent 1')
    fireEvent.click(screen.getByTestId('title-regenerate'))
    expect(screen.getByTestId('title-input')).toHaveValue('Agent 2')
  })

  it('shows no error for a valid title', () => {
    renderTitleInput()
    expect(screen.queryByText('Name must not be empty')).toBeNull()
  })

  it('shows the error once the field is emptied', () => {
    renderTitleInput()
    fireEvent.input(screen.getByTestId('title-input'), { target: { value: '' } })
    expect(screen.getByText('Name must not be empty')).toBeInTheDocument()
  })

  // The error must CLEAR again, not stick once shown -- otherwise the user
  // repairs the field and the dialog still reads as broken.
  it('clears the error when the title becomes valid again', () => {
    renderTitleInput()
    const input = screen.getByTestId('title-input')
    fireEvent.input(input, { target: { value: '' } })
    expect(screen.getByText('Name must not be empty')).toBeInTheDocument()
    fireEvent.input(input, { target: { value: 'Agent Tim' } })
    expect(screen.queryByText('Name must not be empty')).toBeNull()
  })

  it('uses the Tooltip component rather than a bare title attribute', () => {
    // A native `title` renders the OS tooltip and, on a control with no
    // aria-label, becomes the accessible name. The lint rule bans it; this
    // asserts the rendered DOM agrees.
    renderTitleInput()
    expect(screen.getByTestId('title-regenerate')).not.toHaveAttribute('title')
  })

  it('does not call the generator again on an ordinary edit', () => {
    const generate = vi.fn(() => 'Agent Gabe')
    renderTitleInput(generate)
    fireEvent.input(screen.getByTestId('title-input'), { target: { value: 'x' } })
    expect(generate).toHaveBeenCalledOnce()
  })
})

describe('titleInput error accessibility', () => {
  // The error also DISABLES Create, so a user who cannot see the red text gets
  // a dead button and no reason. The input must therefore report itself
  // invalid and point at the message.
  it('links the error to the input and announces it', () => {
    renderTitleInput()
    const input = screen.getByLabelText('Title')
    fireEvent.input(input, { target: { value: '   ' } })

    expect(input).toHaveAttribute('aria-invalid', 'true')
    const describedBy = input.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()
    const message = document.getElementById(describedBy!)
    expect(message).toHaveTextContent('Name must not be empty')
    expect(message).toHaveAttribute('role', 'alert')
  })

  it('marks the input valid and drops the link while the title is acceptable', () => {
    renderTitleInput()
    const input = screen.getByLabelText('Title')

    expect(input).not.toHaveAttribute('aria-invalid')
    expect(input).not.toHaveAttribute('aria-describedby')
  })
})
