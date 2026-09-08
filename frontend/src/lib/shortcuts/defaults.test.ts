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

  // `Control` and not `$mod`: macOS reserves Command+` for its window cycler,
  // so the chord would never reach the page there.
  it('gives Control+grave to the quake terminal inside agent tabs', () => {
    expect(WORKSPACE_KEYBINDINGS.filter(b => b.command === 'terminal.toggleQuake')).toEqual([
      {
        key: 'Control+grave',
        command: 'terminal.toggleQuake',
        when: 'activeTabType == "agent" && !dialogOpen',
      },
    ])
  })

  // Enter and Cmd+Enter in the composer already send, so the command keeps no
  // default CHORD -- but it keeps its ENTRY, and the empty key is what says so.
  //
  // The entry is load-bearing. `mergeKeybindings` gives a user override
  // `when: o.when ?? def.when`, so a command absent from this table hands every
  // rebinding of it a clause of `undefined`: the chord would then resolve
  // everywhere, and `activateBindings` calls preventDefault for any binding
  // that resolves. A user who bound Send Message to `$mod+k` would find `$mod+k`
  // swallowed in the terminal and in every other surface.
  it('leaves send message with no chord but keeps the clause a rebinding inherits', () => {
    expect(ALL_DEFAULTS.filter(b => b.command === 'chat.sendMessage')).toEqual([
      { key: '', command: 'chat.sendMessage', when: 'chatInputFocused' },
    ])
  })

  it('binds no key twice in the same when-context', () => {
    const seen = new Map<string, string>()
    // An empty key is "no chord by default", not a chord, so several commands
    // may carry one without colliding.
    for (const b of ALL_DEFAULTS.filter(b => b.key !== '')) {
      const slot = JSON.stringify([b.key, b.when ?? ''])
      const holder = seen.get(slot)
      expect(holder, `${b.key} is bound to both ${holder} and ${b.command}`).toBeUndefined()
      seen.set(slot, b.command)
    }
  })
})
