import { afterEach, describe, expect, it } from 'vitest'
import { CONTENT_EDITABLE_SELECTOR, disableTextSubstitutions, INPUT_OR_EDITABLE_SELECTOR, isContentEditableElement, isTypingContext, isTypingElement } from '~/lib/textInputBehavior'

/**
 * An element that carries `contenteditable` set to `value`.
 *
 * No `tabindex` is needed to focus it: jsdom makes any element that CARRIES
 * the attribute focusable, whatever the value says (`isFocusableAreaElement`
 * asks `hasAttributeNS(null, 'contenteditable')` alone). So each spelling
 * below reaches the predicates with focus really on the element.
 */
function editable(value: string): HTMLElement {
  const el = document.createElement('div')
  el.setAttribute('contenteditable', value)
  return el
}

/**
 * An element that inherits editing from an editable ancestor.
 *
 * jsdom implements `isContentEditable` on nothing, so the property is
 * defined here by hand. A browser sets it for real: a focusable descendant
 * of an editing host reports `isContentEditable === true` while its own
 * `contenteditable` attribute is absent (verified in Chromium), which is the
 * case no attribute comparison can answer.
 */
function inherited(): HTMLElement {
  const el = document.createElement('div')
  el.setAttribute('tabindex', '0')
  Object.defineProperty(el, 'isContentEditable', { value: true, configurable: true })
  return el
}

describe('isContentEditableElement', () => {
  it('reports the three editable spellings of the attribute', () => {
    expect(isContentEditableElement(editable('true'))).toBe(true)
    // The HTML shorthand for the true state.
    expect(isContentEditableElement(editable(''))).toBe(true)
    // What a plain-text editing host uses.
    expect(isContentEditableElement(editable('plaintext-only'))).toBe(true)
  })

  it('reports contenteditable="false" and a missing attribute as not editable', () => {
    expect(isContentEditableElement(editable('false'))).toBe(false)
    expect(isContentEditableElement(document.createElement('div'))).toBe(false)
  })

  // `inherit` is the attribute's own word for "ask my ancestors", and an
  // element outside an editing host inherits nothing.
  it('reports an unrecognised attribute value as not editable', () => {
    expect(isContentEditableElement(editable('inherit'))).toBe(false)
    expect(isContentEditableElement(editable('TRUE'))).toBe(false)
  })

  it('reports an element that inherits editing from an ancestor', () => {
    expect(isContentEditableElement(inherited())).toBe(true)
  })
})

describe('editable-host selector (CONTENT_EDITABLE_SELECTOR)', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  // The selector form and the predicate form must agree on the attribute,
  // because the pointer gestures ask with one and the shortcut system asks
  // with the other. A press the drag sensor declines and a keystroke the
  // shortcut system claims would be the same element answered two ways.
  it('matches every spelling the predicate calls editable, and nothing else', () => {
    for (const value of ['true', '', 'plaintext-only']) {
      const el = editable(value)
      document.body.append(el)
      expect(el.matches(CONTENT_EDITABLE_SELECTOR), `contenteditable="${value}"`).toBe(true)
      expect(isContentEditableElement(el)).toBe(true)
    }
    for (const el of [editable('false'), editable('inherit'), document.createElement('div')]) {
      document.body.append(el)
      expect(el.matches(CONTENT_EDITABLE_SELECTOR)).toBe(false)
      expect(isContentEditableElement(el)).toBe(false)
    }
  })

  // The gestures ask with `closest`, from whatever descendant the press
  // landed on. Walking up is what makes inherited editing answerable in the
  // selector form, which cannot read `isContentEditable`.
  it('finds the editing host from a descendant the press landed on', () => {
    const host = editable('plaintext-only')
    const inner = document.createElement('span')
    host.append(inner)
    document.body.append(host)

    expect(inner.closest(CONTENT_EDITABLE_SELECTOR)).toBe(host)
  })
})

describe('embedded-UI fragment (INPUT_OR_EDITABLE_SELECTOR)', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('matches an input, a textarea and every editable spelling', () => {
    const members = [
      document.createElement('input'),
      document.createElement('textarea'),
      editable(''),
      editable('true'),
      editable('plaintext-only'),
    ]
    for (const el of members) {
      document.body.append(el)
      expect(el.matches(INPUT_OR_EDITABLE_SELECTOR), el.outerHTML).toBe(true)
    }
  })

  // Wider than `TEXT_ENTRY_INPUT_TYPES`, which narrows the same elements for
  // the substitutions pass. A pointer gesture wants every INPUT: a press on a
  // checkbox belongs to the checkbox, whether or not it holds text.
  it('matches an input whatever its type', () => {
    for (const type of ['checkbox', 'range', 'color', 'file']) {
      const el = document.createElement('input')
      el.type = type
      document.body.append(el)
      expect(el.matches(INPUT_OR_EDITABLE_SELECTOR), `type="${type}"`).toBe(true)
    }
  })

  // `select`, `button` and `[data-drag-handle]` belong to the guard in
  // ~/lib/dragActivators.ts alone. Moving one of them down into this fragment
  // would apply it to the context-menu gesture and the drag sensor too, and
  // both of those must still act on all three.
  it('matches nothing that one guard adds on its own', () => {
    const grip = document.createElement('span')
    grip.setAttribute('data-drag-handle', '')
    const outsiders = [
      document.createElement('select'),
      document.createElement('button'),
      document.createElement('div'),
      editable('false'),
      grip,
    ]
    for (const el of outsiders) {
      document.body.append(el)
      expect(el.matches(INPUT_OR_EDITABLE_SELECTOR), el.outerHTML).toBe(false)
    }
  })
})

describe('isTypingElement', () => {
  it('reports an input, a textarea and a select', () => {
    for (const tag of ['input', 'textarea', 'select'] as const)
      expect(isTypingElement(document.createElement(tag))).toBe(true)
  })

  it('reports an editable element, whichever spelling it uses', () => {
    for (const value of ['true', '', 'plaintext-only'])
      expect(isTypingElement(editable(value))).toBe(true)
    expect(isTypingElement(inherited())).toBe(true)
  })

  it('reports a button, a plain element and null as not typing', () => {
    expect(isTypingElement(document.createElement('button'))).toBe(false)
    expect(isTypingElement(document.createElement('div'))).toBe(false)
    expect(isTypingElement(editable('false'))).toBe(false)
    expect(isTypingElement(null)).toBe(false)
  })
})

/**
 * The predicate two callers depend on: the `inputFocused` lazy context that
 * every when-expression reads through `useShortcuts`, and the preferences
 * search box, which binds a bare `/` and asks this directly. A branch that
 * answers wrong either steals a keystroke out of an input or drops a
 * shortcut the user pressed outside one.
 */
describe('isTypingContext', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  /** Focus `el` inside the document, then ask the predicate. */
  function focused(el: HTMLElement): boolean {
    document.body.append(el)
    el.focus()
    // The branch under test is reached only while focus really landed, so
    // a jsdom element that refuses focus fails here and not on the answer.
    expect(document.activeElement).toBe(el)
    return isTypingContext()
  }

  it('reports a focused input as typing', () => {
    expect(focused(document.createElement('input'))).toBe(true)
  })

  it('reports a focused textarea as typing', () => {
    expect(focused(document.createElement('textarea'))).toBe(true)
  })

  // A SELECT takes no text, but a single-key shortcut still must not fire
  // while it has focus: the key belongs to its own type-ahead.
  it('reports a focused select as typing', () => {
    expect(focused(document.createElement('select'))).toBe(true)
  })

  it('reports a focused contenteditable element as typing', () => {
    expect(focused(editable('true'))).toBe(true)
  })

  it('reports contenteditable="false" as not typing', () => {
    expect(focused(editable('false'))).toBe(false)
  })

  // Both spellings take the user's keystrokes, so a shortcut that fires
  // into them is the defect this predicate exists to stop.
  it('reports the empty and plaintext-only spellings as typing', () => {
    expect(focused(editable(''))).toBe(true)
    document.body.replaceChildren()
    expect(focused(editable('plaintext-only'))).toBe(true)
  })

  it('reports a focused plain element as not typing', () => {
    const el = document.createElement('div')
    // A bare <div> takes focus in neither a browser nor jsdom. The
    // `tabindex` is what makes one focusable, and that is the plain
    // element a user can actually reach with Tab.
    el.setAttribute('tabindex', '0')
    expect(focused(el)).toBe(false)
  })

  it('reports a focused button as not typing', () => {
    expect(focused(document.createElement('button'))).toBe(false)
  })

  // jsdom never answers null here: focus falls back to <body>, so the null
  // branch is reachable only with the property replaced. A real document
  // does answer null -- one whose <body> was removed, and an <iframe>
  // document before it loads, both do.
  it('reports no typing while the document has no active element', () => {
    Object.defineProperty(document, 'activeElement', { configurable: true, get: () => null })
    try {
      expect(isTypingContext()).toBe(false)
    }
    finally {
      Reflect.deleteProperty(document, 'activeElement')
    }
    expect(document.activeElement).toBe(document.body)
  })
})

describe('disableTextSubstitutions', () => {
  it('applies attrs to text-entry inputs and textareas', () => {
    const root = document.createElement('div')
    root.innerHTML = `
      <input type="text" />
      <textarea></textarea>
      <input type="checkbox" />
    `

    disableTextSubstitutions(root)

    const textInput = root.querySelector<HTMLInputElement>('input[type="text"]')!
    const textarea = root.querySelector('textarea')!
    const checkbox = root.querySelector<HTMLInputElement>('input[type="checkbox"]')!

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
    const editableEl = document.createElement('div')
    editableEl.setAttribute('contenteditable', 'true')
    root.appendChild(editableEl)

    disableTextSubstitutions(root)

    expect(editableEl).toHaveAttribute('autocorrect', 'off')
    expect(editableEl).toHaveAttribute('autocapitalize', 'off')
    expect(editableEl.spellcheck).toBe(false)
  })

  it('applies attrs to the empty and plaintext-only spellings', () => {
    const root = document.createElement('div')
    const empty = editable('')
    const plaintext = editable('plaintext-only')
    root.append(empty, plaintext)

    disableTextSubstitutions(root)

    for (const el of [empty, plaintext]) {
      expect(el).toHaveAttribute('autocorrect', 'off')
      expect(el).toHaveAttribute('autocapitalize', 'off')
      expect(el.spellcheck).toBe(false)
    }
  })

  it('leaves a contenteditable="false" element alone', () => {
    const root = document.createElement('div')
    const el = editable('false')
    root.append(el)

    disableTextSubstitutions(root)

    expect(el).not.toHaveAttribute('autocorrect')
    expect(el).not.toHaveAttribute('autocapitalize')
  })

  // The focusin handler in `app.tsx` passes the focused element itself, not
  // a container, so the root has to be treated as a candidate of its own.
  it('applies attrs to a root that is itself a text-entry element', () => {
    const input = document.createElement('input')
    disableTextSubstitutions(input)
    expect(input).toHaveAttribute('autocorrect', 'off')
    expect(input).toHaveAttribute('autocapitalize', 'off')
    expect(input.spellcheck).toBe(false)
  })

  // A SELECT holds no free text, so autocorrect has nothing to act on --
  // the one place where the narrow question and `isTypingElement` differ.
  it('leaves a select alone although it counts as a typing element', () => {
    const select = document.createElement('select')
    expect(isTypingElement(select)).toBe(true)

    disableTextSubstitutions(select)

    expect(select).not.toHaveAttribute('autocorrect')
    expect(select).not.toHaveAttribute('autocapitalize')
  })
})
