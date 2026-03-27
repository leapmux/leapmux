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

    expect(textInput.getAttribute('autocorrect')).toBe('off')
    expect(textInput.getAttribute('autocapitalize')).toBe('off')
    expect(textInput.spellcheck).toBe(false)

    expect(textarea.getAttribute('autocorrect')).toBe('off')
    expect(textarea.getAttribute('autocapitalize')).toBe('off')
    expect(textarea.spellcheck).toBe(false)

    expect(checkbox.getAttribute('autocorrect')).toBeNull()
    expect(checkbox.getAttribute('autocapitalize')).toBeNull()
  })

  it('applies attrs to contenteditable elements', () => {
    const root = document.createElement('div')
    const editable = document.createElement('div')
    editable.setAttribute('contenteditable', 'true')
    root.appendChild(editable)

    disableTextSubstitutions(root)

    expect(editable.getAttribute('autocorrect')).toBe('off')
    expect(editable.getAttribute('autocapitalize')).toBe('off')
    expect(editable.spellcheck).toBe(false)
  })
})
