import { describe, expect, it } from 'vitest'
import { disableTextSubstitutions } from '~/lib/textInputBehavior'

describe('disableTextSubstitutions', () => {
  it('applies attrs to text-entry inputs and textareas', () => {
    const root = document.createElement('div')
    root.innerHTML = `
      <input type="text" />
      <textarea></textarea>
      <input type="checkbox" />
    `

    disableTextSubstitutions(root)

    const textInput = root.querySelector('input[type="text"]')!
    const textarea = root.querySelector('textarea')!
    const checkbox = root.querySelector('input[type="checkbox"]')!

    expect(textInput).toHaveAttribute('autocorrect', 'off')
    expect(textInput).toHaveAttribute('autocapitalize', 'off')
    expect(textInput.spellcheck).toBe(false)

    expect(textarea).toHaveAttribute('autocorrect', 'off')
    expect(textarea).toHaveAttribute('autocapitalize', 'off')
    expect(textarea.spellcheck).toBe(false)

    expect(checkbox).not.toHaveAttribute('autocorrect')
    expect(checkbox).not.toHaveAttribute('autocapitalize')
  })

  it('applies attrs to contenteditable elements', () => {
    const root = document.createElement('div')
    const editable = document.createElement('div')
    editable.setAttribute('contenteditable', 'true')
    root.appendChild(editable)

    disableTextSubstitutions(root)

    expect(editable).toHaveAttribute('autocorrect', 'off')
    expect(editable).toHaveAttribute('autocapitalize', 'off')
    expect(editable.spellcheck).toBe(false)
  })
})
