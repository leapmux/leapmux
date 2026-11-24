import { describe, expect, it } from 'vitest'
import { QODER_MODE } from '~/generated/contracts/qoder-protocol'
import { qoderControls } from './pluginControls'

describe('qoderControls', () => {
  // The Worker reads each request, builds each answer, and records the decision that
  // words a saved answer, so the browser keeps only the presets.
  it('registers only the presets', () => {
    expect(Object.keys(qoderControls)).toEqual(['permissionPresets'])
  })
})

// Auto approves safe calls and asks about the rest. Bypass is Qoder's own
// bypassPermissions mode. Don't Ask is neither: it denies the calls that Qoder
// does not approve in advance.
describe('qoderControls permissionPresets', () => {
  it('maps Smart to Auto and Bypass to the native bypass mode', () => {
    expect(qoderControls.permissionPresets).toEqual({
      smart: {
        sets: { permissionMode: QODER_MODE.Auto },
      },
      bypass: {
        sets: { permissionMode: QODER_MODE.BypassPermissions },
      },
    })
  })
})
