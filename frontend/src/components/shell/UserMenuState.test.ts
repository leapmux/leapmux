import { describe, expect, it } from 'vitest'
import { closePreferences, openPreferences, preferencesOpenSeq, showPreferencesDialog } from './UserMenuState'

describe('openPreferences', () => {
  it('opens on the requested category', () => {
    openPreferences('admin-email')
    expect(showPreferencesDialog()).toBe('admin-email')
    closePreferences()
    expect(showPreferencesDialog()).toBeNull()
  })

  // The three entry points all pass 'appearance'. Writing the same string
  // into the category signal notifies nothing, so the dialog's deep-link
  // effect never re-ran and the dialog stayed on whatever section the user
  // had picked. The count is what changes on a repeat request.
  it('advances the request count even for the category already open', () => {
    openPreferences('appearance')
    const first = preferencesOpenSeq()
    openPreferences('appearance')
    expect(showPreferencesDialog()).toBe('appearance')
    expect(preferencesOpenSeq()).toBe(first + 1)
    closePreferences()
  })
})
