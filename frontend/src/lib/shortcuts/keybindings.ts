import type { Keybinding, UserKeybindingOverride } from './types'
import { createSignal } from 'solid-js'
import { tinykeys } from 'tinykeys'
import { createLogger } from '~/lib/logger'
import { executeCommand } from './commands'
import { evaluateWhen, getContext, whenReferencesKey } from './context'

const log = createLogger('shortcuts')

/**
 * Keys that no binding may claim, whatever a user writes by hand in
 * `custom_keybindings_json`.
 *
 * `activateBindings` dispatches from a capture-phase listener on `window`,
 * which is the first handler on the whole propagation path. A binding for
 * Escape therefore preempts every layer that dismisses itself on Escape -- an
 * open `DropdownMenu`, the Preferences search box, an inline rename -- so one
 * press dismisses two layers at once. The browser already aims a close request
 * at the innermost open layer, and `Dialog` acts on the request it receives.
 *
 * A chord that merely CONTAINS Escape (`Shift+Escape`) is not reserved: the
 * browser makes no close request for it, so it displaces no layer.
 *
 * The settings editor cannot produce one of these either -- `captureKeydown`
 * in `KeybindingsControl` reads Escape as "stop capturing". This guard covers
 * the hand-edited JSON, which reaches the merge without passing that editor.
 */
const RESERVED_KEYS: ReadonlySet<string> = new Set(['Escape'])

/**
 * Merge default keybindings with user overrides.
 *
 * Multiple overrides for the same command are supported — each non-empty-key
 * override becomes a separate binding (e.g. to bind the same command to
 * different keys with different when-clauses).
 *
 * For each command with overrides:
 * - The default entry is replaced by all non-empty-key overrides
 * - Each override inherits the default's when-clause if it doesn't specify one
 * - If all overrides have empty keys, the command is fully unbound
 *
 * Overrides for commands not in defaults are appended as new bindings.
 *
 * An override for a reserved key is dropped, and the command keeps whatever
 * the defaults give it. Dropping the one entry beats unbinding the command:
 * a single bad line in the JSON costs the user that line, not the shortcut.
 */
export function mergeKeybindings(
  defaults: readonly Keybinding[],
  overrides: readonly UserKeybindingOverride[],
): Keybinding[] {
  const overrideMap = new Map<string, UserKeybindingOverride[]>()
  for (const o of overrides) {
    if (RESERVED_KEYS.has(o.key)) {
      log.warn('Ignoring an override that binds a reserved key', { key: o.key, command: o.command })
      continue
    }
    let list = overrideMap.get(o.command)
    if (!list) {
      list = []
      overrideMap.set(o.command, list)
    }
    list.push(o)
  }

  const result: Keybinding[] = []
  const processedCommands = new Set<string>()

  for (const def of defaults) {
    const commandOverrides = overrideMap.get(def.command)
    if (commandOverrides) {
      processedCommands.add(def.command)
      for (const o of commandOverrides) {
        if (o.key === '')
          continue
        result.push({
          key: o.key,
          command: def.command,
          when: o.when ?? def.when,
          args: def.args,
        })
      }
    }
    else if (def.key !== '') {
      result.push({ ...def })
    }
    // An empty default key means the command has NO default chord, and the
    // entry exists only to declare the `when` clause every user binding of it
    // inherits above. Emitting it would register a binding on the empty key.
  }

  for (const [command, commandOverrides] of overrideMap) {
    if (processedCommands.has(command))
      continue
    for (const o of commandOverrides) {
      if (o.key === '')
        continue
      result.push({
        key: o.key,
        command: o.command,
        when: o.when,
      })
    }
  }

  return result
}

interface BindingGroup {
  key: string
  bindings: Keybinding[]
}

/** Group keybindings by their key string. */
export function groupBindings(bindings: readonly Keybinding[]): BindingGroup[] {
  const map = new Map<string, Keybinding[]>()
  for (const b of bindings) {
    let group = map.get(b.key)
    if (!group) {
      group = []
      map.set(b.key, group)
    }
    group.push(b)
  }
  return Array.from(map.entries(), ([key, bindings]) => ({ key, bindings }))
}

const MODIFIER_RE = /\$mod|Control|Alt|Meta|Shift/
export const FUNCTION_KEY_RE = /^F(?:[1-9]|1[0-2])$/
const SINGLE_LETTER_RE = /^[a-z]$/i

/** Check if a key string contains modifier keys. */
function hasModifier(key: string): boolean {
  const first = key.split(' ')[0]
  return MODIFIER_RE.test(first)
}

/**
 * The physical key positions one AUTHOR-FACING key name can arrive on.
 *
 * A binding names a key the user presses, and for punctuation that name is not
 * the character on the keycap: the character moves between physical positions
 * from one layout to the next, and the browsers disagree about which position
 * reports which `event.code`. So the author writes the intent (`grave` = "the
 * key under Esc") and this table expands it to every code that intent can
 * produce, as a tinykeys `(<regex>)` matcher -- which tinykeys tests against
 * BOTH `event.key` and `event.code`.
 *
 * `grave` accepts two codes, and both are needed:
 *
 *   - `Backquote` is the W3C name for the key left of `1`, "``~` on a US
 *     keyboard" (https://www.w3.org/TR/uievents-code/). It is what Chrome and
 *     Firefox report there on every layout, including the German `^`, the
 *     French `²` and the Spanish `º` -- none of which produce a backquote
 *     CHARACTER, and the first of which is a dead key whose `event.key` is
 *     `Dead`. Matching the code rather than the character is the same reason
 *     single letters become `KeyX` below.
 *   - `IntlBackslash` is what WebKit reports for that same physical key on a
 *     macOS ISO keyboard. macOS swaps the scan codes of `kVK_ANSI_Grave` and
 *     `kVK_ISO_Section` on an ISO keyboard; Chrome and Firefox unswap them to
 *     follow the spec, and WebKit does not
 *     (https://bugs.webkit.org/show_bug.cgi?id=244202, open since 2022). The
 *     desktop app is WKWebView on macOS, so without this the shortcut is dead
 *     there for every European Mac.
 *
 * The cost is that on an ISO keyboard the extra key beside the left Shift also
 * fires the binding. That is one spare key, behind a modifier, and it buys a
 * shortcut that works in every engine -- the alternative needs the host's
 * keyboard layout, which `navigator.keyboard` supplies on Chromium alone.
 *
 * A JIS keyboard is the one layout this cannot serve: there `Backquote` is the
 * 半角/全角 key, which the IME consumes. The command stays rebindable.
 */
const PHYSICAL_KEY_ALIASES: Record<string, string> = {
  grave: '(Backquote|IntlBackslash)',
}

/**
 * The canonical author-facing name for an `event.code`, when one physical key
 * has several codes. Used by the capture path in Preferences, so a rebinding
 * records the INTENT rather than whichever code the current engine and layout
 * happened to report -- otherwise a chord captured in Chrome would not fire in
 * the desktop app on the same machine.
 */
export function physicalKeyAliasFor(code: string): string | undefined {
  for (const [name, pattern] of Object.entries(PHYSICAL_KEY_ALIASES)) {
    if (pattern.slice(1, -1).split('|').includes(code))
      return name
  }
  return undefined
}

/**
 * Convert a key part to the form tinykeys matches on.
 *
 * Single letters become their `KeyX` event.code form so tinykeys matches by
 * physical key position. tinykeys compares against `event.key` for literal
 * letters, which fails on macOS WebKit when Option transforms the character
 * (e.g. Cmd+Alt+N produces `event.key = '\u02dc'`). `event.code` stays `KeyN`
 * regardless of the Option transformation or keyboard layout.
 *
 * A name in `PHYSICAL_KEY_ALIASES` becomes the set of codes that key can
 * report -- the same problem one level further out, for punctuation whose
 * position moves between layouts and engines.
 */
function toTinykeysKey(key: string): string {
  return key
    .split(' ')
    .map(chord =>
      chord
        .split('+')
        .map((part) => {
          const alias = PHYSICAL_KEY_ALIASES[part.toLowerCase()]
          if (alias !== undefined)
            return alias
          return SINGLE_LETTER_RE.test(part) ? `Key${part.toUpperCase()}` : part
        })
        .join('+'),
    )
    .join(' ')
}

/** Check if a key string is a plain function key like F5 or F12. */
function isPlainFunctionKey(key: string): boolean {
  const firstChord = key.split(' ')[0]
  const parts = firstChord.split('+')
  return parts.length === 1 && FUNCTION_KEY_RE.test(parts[0])
}

/**
 * Resolve which binding to execute for a given key event.
 * Returns the command ID to execute, or null if no match.
 */
export function resolve(bindings: readonly Keybinding[], key: string): string | null {
  const inputFocused = !!getContext('inputFocused')
  const modifier = hasModifier(key)
  const dedicated = isPlainFunctionKey(key)

  for (const binding of bindings) {
    // Non-modifier shortcuts are suppressed when input is focused,
    // unless the when-clause explicitly references inputFocused.
    if (!modifier && !dedicated && inputFocused && !whenReferencesKey(binding.when, 'inputFocused'))
      continue

    if (evaluateWhen(binding.when))
      return binding.command
  }

  return null
}

// Each slot has its own tinykeys instance so independent activation sites
// (App root vs AppShell) don't stomp on each other. The merged
// `activeBindings` signal feeds menu hint lookups across both.
type BindingSlot = 'core' | 'workspace'

interface SlotState {
  unsubscribe: () => void
  bindings: readonly Keybinding[]
}
const slotState = new Map<BindingSlot, SlotState>()
// Reactive so JSX expressions that read bindings (menu shortcut hints,
// tooltips) update once `activateBindings` runs.
const [activeBindings, setActiveBindings] = createSignal<readonly Keybinding[]>([])

function recomputeActiveBindings(): void {
  const all: Keybinding[] = []
  for (const state of slotState.values())
    all.push(...state.bindings)
  setActiveBindings(all)
}

export function activateBindings(bindings: readonly Keybinding[], slot: BindingSlot): void {
  slotState.get(slot)?.unsubscribe()

  const groups = groupBindings(bindings)
  const keyMap: Record<string, (e: KeyboardEvent) => void> = {}

  for (const group of groups) {
    keyMap[toTinykeysKey(group.key)] = (e: KeyboardEvent) => {
      // Skip dispatch while an IME composition is active so CJK input is not
      // hijacked by modifier shortcuts that share keys with composition commits.
      if (e.isComposing)
        return
      const commandId = resolve(group.bindings, group.key)
      if (commandId) {
        e.preventDefault()
        e.stopPropagation()
        executeCommand(commandId)
      }
    }
  }

  // tinykeys 4 added a default `ignore` filter (defaultKeybindingsHandlerIgnore)
  // that drops keydown events whose target is an input/select/textarea/
  // contenteditable element. Because we bind at `window` and the user is almost
  // always focused inside the chat input (ProseMirror contenteditable), a
  // terminal (xterm textarea), or a form field, that filter would swallow nearly
  // every shortcut before it reaches us. We already do focus-aware filtering in
  // resolve() (and skip IME composition in the handler above), so disable the
  // built-in ignore and let every event through.
  const unsubscribe = tinykeys(window, keyMap, { capture: true, ignore: () => false })
  slotState.set(slot, { unsubscribe, bindings })
  recomputeActiveBindings()
  log.debug(`Bound ${groups.length} key groups (${bindings.length} bindings) for slot=${slot}`)
}

export function unbindAll(slot?: BindingSlot): void {
  if (slot) {
    slotState.get(slot)?.unsubscribe()
    slotState.delete(slot)
  }
  else {
    for (const state of slotState.values())
      state.unsubscribe()
    slotState.clear()
  }
  recomputeActiveBindings()
}

/** Get all active key strings for a command ID, preferring currently-enabled bindings. */
export function getBindingsForCommand(commandId: string): string[] {
  const active: string[] = []
  const fallback: string[] = []

  const addUnique = (keys: string[], key: string) => {
    if (!keys.includes(key))
      keys.push(key)
  }

  for (const b of activeBindings()) {
    if (b.command === commandId) {
      if (evaluateWhen(b.when))
        addUnique(active, b.key)
      else
        addUnique(fallback, b.key)
    }
  }
  return active.length > 0 ? active : fallback
}
