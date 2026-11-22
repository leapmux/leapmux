import { afterEach, describe, expect, it } from 'vitest'
import { createModalCover } from './modalCover'

afterEach(() => {
  document.body.innerHTML = ''
})

describe('createModalCover', () => {
  it('marks open non-inert dialogs inert, skips pre-inert dialogs, and restores focus on uncover', () => {
    const openDialog = document.createElement('dialog')
    openDialog.setAttribute('open', '')
    const button = document.createElement('button')
    openDialog.appendChild(button)

    const alreadyInertDialog = document.createElement('dialog')
    alreadyInertDialog.setAttribute('open', '')
    alreadyInertDialog.setAttribute('inert', '')

    const closedDialog = document.createElement('dialog')

    document.body.append(openDialog, alreadyInertDialog, closedDialog)
    button.focus()
    expect(document.activeElement).toBe(button)

    const cover = createModalCover()
    cover.cover()

    expect(openDialog).toHaveAttribute('inert')
    expect(alreadyInertDialog).toHaveAttribute('inert')
    expect(closedDialog).not.toHaveAttribute('inert')

    // Calling cover() again while active is a no-op and does not capture a
    // newly opened sibling dialog (such as the host's own dialog).
    const hostDialog = document.createElement('dialog')
    hostDialog.setAttribute('open', '')
    document.body.appendChild(hostDialog)
    cover.cover()
    expect(hostDialog).not.toHaveAttribute('inert')

    cover.uncover()
    expect(openDialog).not.toHaveAttribute('inert')
    expect(alreadyInertDialog).toHaveAttribute('inert')
    expect(document.activeElement).toBe(button)
  })

  it('ignores uncover when inactive and tolerates a detached focus target', () => {
    const dialog = document.createElement('dialog')
    dialog.setAttribute('open', '')
    const button = document.createElement('button')
    dialog.appendChild(button)
    document.body.appendChild(dialog)
    button.focus()

    const cover = createModalCover()
    cover.uncover()
    expect(dialog).not.toHaveAttribute('inert')

    cover.cover()
    expect(dialog).toHaveAttribute('inert')
    button.remove()

    expect(() => cover.uncover()).not.toThrow()
    expect(dialog).not.toHaveAttribute('inert')
  })
})
