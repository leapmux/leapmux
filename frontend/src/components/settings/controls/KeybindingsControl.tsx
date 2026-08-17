import type { Component } from 'solid-js'
import type { Keybinding, UserKeybindingOverride } from '~/lib/shortcuts/types'
import { createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js'
import { usePreferences } from '~/context/PreferencesContext'
import { getAllCommands } from '~/lib/shortcuts/commands'
import { CORE_KEYBINDINGS, WORKSPACE_KEYBINDINGS } from '~/lib/shortcuts/defaults'
import { formatShortcut } from '~/lib/shortcuts/display'
import { mergeKeybindings } from '~/lib/shortcuts/keybindings'
import { getPlatform } from '~/lib/shortcuts/platform'
import { errorText } from '~/styles/shared.css'

/** Backend cap on stored overrides (usersettings.MaxKeybindings). */
export const MAX_KEYBINDING_OVERRIDES = 200

interface CommandRow {
  command: string
  title: string
  category: string
  /** Effective keys after merging defaults with the user's overrides. */
  keys: string[]
  customized: boolean
  /** The default binding's when-clause, inherited by new overrides. */
  when?: string
}

/**
 * Build the merged command table: every registered command plus any default
 * binding whose command is not (yet) registered, each with its effective keys
 * and whether an override customizes it. Pure, for testing.
 */
export function buildCommandRows(
  defaults: readonly Keybinding[],
  overrides: readonly UserKeybindingOverride[],
  registered: { id: string, title: string, category?: string }[],
): CommandRow[] {
  const merged = mergeKeybindings(defaults, overrides)
  const defaultWhen = new Map<string, string | undefined>()
  const commands = new Map<string, { title: string, category: string }>()
  for (const d of defaults) {
    if (!defaultWhen.has(d.command))
      defaultWhen.set(d.command, d.when)
    if (!commands.has(d.command))
      commands.set(d.command, { title: d.command, category: 'Other' })
  }
  for (const c of registered) {
    commands.set(c.id, { title: c.title, category: c.category ?? 'Other' })
    if (!defaultWhen.has(c.id))
      defaultWhen.set(c.id, undefined)
  }
  const overridden = new Set(overrides.map(o => o.command))
  // Group once, then look up per command. Scanning `merged` inside the
  // command loop made the build quadratic in the command count, and both
  // lists hold every registered command.
  const keysByCommand = new Map<string, string[]>()
  for (const b of merged) {
    const keys = keysByCommand.get(b.command)
    if (keys)
      keys.push(b.key)
    else
      keysByCommand.set(b.command, [b.key])
  }
  const rows: CommandRow[] = []
  for (const [command, meta] of commands) {
    const keys = keysByCommand.get(command) ?? []
    rows.push({
      command,
      title: meta.title,
      category: meta.category,
      keys,
      customized: overridden.has(command),
      when: defaultWhen.get(command),
    })
  }
  rows.sort((a, b) => a.category.localeCompare(b.category) || a.title.localeCompare(b.title))
  return rows
}

/**
 * Render a captured keyboard event as the tinykeys-style key string the
 * defaults use ('$mod+Shift+n'). Null for a press that is only modifiers (the
 * user is still composing) or a key we do not bind.
 */
export function chordFromEvent(e: KeyboardEvent): string | null {
  if (['Meta', 'Control', 'Alt', 'Shift'].includes(e.key))
    return null
  const platform = getPlatform()
  const parts: string[] = []
  if (platform === 'mac' ? e.metaKey : e.ctrlKey)
    parts.push('$mod')
  if (platform === 'mac' && e.ctrlKey)
    parts.push('Control')
  if (e.altKey)
    parts.push('Alt')
  if (e.shiftKey)
    parts.push('Shift')
  let key: string | null = null
  if (/^[a-z0-9]$/i.test(e.key)) {
    // The defaults' convention: letters and digits as the literal key
    // (lowercase), everything else by its event.code name (Comma,
    // BracketLeft, ArrowLeft, ...) — which is also what tinykeys matches on.
    key = e.key.toLowerCase()
  }
  else if (e.code !== '') {
    key = e.code
  }
  else if (e.key.length > 1) {
    key = e.key
  }
  if (key === null)
    return null
  return [...parts, key].join('+')
}

/** All default bindings, core slot first like the runtime activation order. */
const ALL_DEFAULTS: readonly Keybinding[] = [...CORE_KEYBINDINGS, ...WORKSPACE_KEYBINDINGS]

/**
 * The keyboard-shortcuts editor: one row per command with its effective
 * binding, a Default/Custom source badge, and a Reset button on customized
 * rows. Clicking a binding captures the next chord; a chord already bound in
 * the same when-context is refused, and the refusal identifies the command
 * that holds it.
 */
export const KeybindingsControl: Component = () => {
  const prefs = usePreferences()
  const [capturing, setCapturing] = createSignal<string | null>(null)
  const [error, setError] = createSignal<string | null>(null)

  const rows = createMemo(() => buildCommandRows(ALL_DEFAULTS, prefs.customKeybindings(), getAllCommands()))

  /**
   * Write the override list and state a refusal inline.
   *
   * `setCustomKeybindings` REJECTS when the hub refuses the write, after
   * restoring the pre-write value — so the row must catch it, or the list
   * reverts with no reason on screen and the rejection is unhandled. Every
   * other row gets this from `SettingRow.commit`; a `custom` editor renders
   * bare, with no binding wrapper around it, so it does its own.
   */
  const write = (next: UserKeybindingOverride[]) => {
    setError(null)
    void Promise.resolve(prefs.setCustomKeybindings(next)).catch((err: unknown) => {
      setError(err instanceof Error ? err.message : String(err))
    })
  }

  const captureKeydown = (e: KeyboardEvent) => {
    const command = capturing()
    if (command === null)
      return
    e.preventDefault()
    e.stopPropagation()
    if (e.key === 'Escape') {
      setCapturing(null)
      setError(null)
      return
    }
    const chord = chordFromEvent(e)
    if (chord === null)
      return
    const row = rows().find(r => r.command === command)
    if (row === undefined) {
      setCapturing(null)
      return
    }
    // Refuse a chord already bound in the same when-context. The message
    // identifies the command that holds the chord, so the user knows which
    // binding the new one would replace.
    const conflict = mergeKeybindings(ALL_DEFAULTS, prefs.customKeybindings())
      .find(b => b.key === chord && b.command !== command && (b.when ?? '') === (row.when ?? ''))
    if (conflict) {
      const conflictRow = rows().find(r => r.command === conflict.command)
      setError(`“${formatShortcut(chord)}” is already bound to ${conflictRow?.title ?? conflict.command}`)
      return
    }
    const next: UserKeybindingOverride[] = prefs.customKeybindings().filter(o => o.command !== command)
    if (next.length >= MAX_KEYBINDING_OVERRIDES) {
      setError(`Too many keybinding overrides (max ${MAX_KEYBINDING_OVERRIDES})`)
      return
    }
    next.push({ key: chord, command, when: row.when })
    setCapturing(null)
    write(next)
  }

  onMount(() => {
    window.addEventListener('keydown', captureKeydown, true)
    onCleanup(() => window.removeEventListener('keydown', captureKeydown, true))
  })

  const resetCommand = (command: string) => {
    write(prefs.customKeybindings().filter(o => o.command !== command))
  }

  return (
    <div class="vstack gap-2">
      <div class="table">
        <table>
          <thead>
            <tr>
              <th>Command</th>
              <th>Binding</th>
              <th>Source</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <For each={rows()}>
              {row => (
                <tr data-command={row.command}>
                  <td>{row.title}</td>
                  <td>
                    <Show
                      when={capturing() === row.command}
                      fallback={(
                        <button
                          type="button"
                          class="small outline"
                          data-testid={`keybinding-${row.command}`}
                          onClick={() => {
                            setCapturing(row.command)
                            setError(null)
                          }}
                        >
                          {row.keys.length > 0 ? row.keys.map(k => formatShortcut(k)).join(' / ') : 'Unbound'}
                        </button>
                      )}
                    >
                      <button type="button" class="small outline" data-testid={`keybinding-capture-${row.command}`}>
                        Press keys…
                      </button>
                    </Show>
                  </td>
                  <td>
                    <span class="badge" data-variant="secondary">
                      {row.customized ? 'Custom' : 'Default'}
                    </span>
                  </td>
                  <td>
                    <Show when={row.customized}>
                      <button
                        type="button"
                        class="small outline"
                        data-testid={`keybinding-reset-${row.command}`}
                        onClick={() => resetCommand(row.command)}
                      >
                        Reset
                      </button>
                    </Show>
                  </td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </div>
      <Show when={error()}>
        <div class={errorText} data-testid="keybinding-error">{error()}</div>
      </Show>
    </div>
  )
}
