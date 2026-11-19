import { describe, expect, it } from 'vitest'
import { CODEBUDDY_MODE } from '~/generated/contracts/codebuddy-protocol'
import { codebuddyControls } from './pluginControls'

describe('codebuddyControls', () => {
  // The Worker reads each request and builds each answer, so the browser keeps only the
  // display of a saved answer and the presets.
  it('registers only the saved-answer display and the presets', () => {
    expect(Object.keys(codebuddyControls).sort()).toEqual(['controlResponseDisplay', 'permissionPresets'])
  })
})

describe('codebuddyControls permissionPresets', () => {
  // CodeBuddy's bypassPermissions and auto are live-switchable modes, so the plus menu and the banner can switch the
  // session onto either. Smart selects Auto, whose classifier decides each call that would prompt.
  it('offers a smart shortcut onto Auto and a bypass shortcut', () => {
    expect(codebuddyControls.permissionPresets?.smart).toStrictEqual({
      sets: { permissionMode: CODEBUDDY_MODE.Auto },
    })
    expect(codebuddyControls.permissionPresets?.bypass).toStrictEqual({
      sets: { permissionMode: CODEBUDDY_MODE.BypassPermissions },
    })
  })
})
