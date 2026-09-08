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

  // The chord the composer's own Cmd+Enter also answers. The `when` is the whole
  // contract: `activateBindings` calls preventDefault ONLY for a binding that
  // resolves, so every conjunct here is what lets a keypress fall through to
  // ProseMirror while the composer holds something to send. Pinned whole rather
  // than by key, because dropping one conjunct would silently swallow messages.
  it('gives the queue steer $mod+Enter only while the composer is empty', () => {
    expect(WORKSPACE_KEYBINDINGS.filter(b => b.key === '$mod+Enter')).toEqual([
      {
        key: '$mod+Enter',
        command: 'chat.steerQueuedInput',
        when: 'activeTabType == "agent" && chatInputEmpty && !terminalFocused && !dialogOpen',
      },
    ])
  })

  it('gives $mod+j to the quake terminal inside agent tabs', () => {
    expect(WORKSPACE_KEYBINDINGS.filter(b => b.key === '$mod+j')).toEqual([
      {
        key: '$mod+j',
        command: 'terminal.toggleQuake',
        when: 'activeTabType == "agent" && !dialogOpen',
      },
    ])
  })

  // Enter and Cmd+Enter in the composer already send, so the command keeps no
  // default chord. It stays REGISTERED, so Preferences still lists it and a user
  // can bind it; its handler resolves the panel through the focused element, so
  // an override with no `when` still does nothing outside a chat panel.
  it('leaves send message unbound, because the composer owns its own chords', () => {
    expect(ALL_DEFAULTS.filter(b => b.command === 'chat.sendMessage')).toEqual([])
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
