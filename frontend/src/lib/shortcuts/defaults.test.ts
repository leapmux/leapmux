import { describe, expect, it } from 'vitest'
import { CORE_KEYBINDINGS, WORKSPACE_KEYBINDINGS } from './defaults'

const ALL_DEFAULTS = [...CORE_KEYBINDINGS, ...WORKSPACE_KEYBINDINGS]

describe('default keybindings', () => {
  it('leaves Escape unbound, so the innermost layer keeps it', () => {
    // The user's half of this rule is enforced, not tested: `RESERVED_KEYS` in
    // ~/lib/shortcuts/keybindings.ts drops an override that claims Escape.
    // These defaults pass through no such filter, so the rule needs a case.
    //
    // `activateBindings` dispatches from a capture-phase listener on `window`,
    // which runs before every handler on the propagation path. An Escape
    // binding therefore preempts each layer that dismisses itself on Escape --
    // an open `DropdownMenu`, the Preferences search box, an inline rename --
    // and one press dismissed the layer AND the dialog under it. The platform
    // aims a close request at the topmost layer instead; `Dialog` acts on the
    // request it receives.
    const escapeBindings = ALL_DEFAULTS.filter(b => b.key === 'Escape')
    expect(escapeBindings).toEqual([])
  })

  it('binds no key twice in the same when-context', () => {
    const seen = new Map<string, string>()
    for (const b of ALL_DEFAULTS) {
      const slot = JSON.stringify([b.key, b.when ?? ''])
      const holder = seen.get(slot)
      expect(holder, `${b.key} is bound to both ${holder} and ${b.command}`).toBeUndefined()
      seen.set(slot, b.command)
    }
  })
})
