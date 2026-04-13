import type { Keybinding, UserKeybindingOverride } from './types'
import tinykeys from 'tinykeys'
import { createLogger } from '~/lib/logger'
import { executeCommand } from './commands'
import { evaluateWhen, getContext } from './context'

const log = createLogger('shortcuts')

// ---------------------------------------------------------------------------
// Merge algorithm
// ---------------------------------------------------------------------------

/**
 * Merge default keybindings with user overrides.
 *
 * For each override:
 * - Find the default with the same command ID → replace key (and when if provided)
 * - If user override key is "", remove the binding (unbind)
 * - Overrides with command IDs not in defaults are appended as new bindings
 */
export function mergeKeybindings(
  defaults: readonly Keybinding[],
  overrides: readonly UserKeybindingOverride[],
): Keybinding[] {
  // Index overrides by command for O(1) lookup
  const overrideMap = new Map<string, UserKeybindingOverride>()
  for (const o of overrides)
    overrideMap.set(o.command, o)

  const result: Keybinding[] = []
  const usedOverrideCommands = new Set<string>()

  for (const def of defaults) {
    const override = overrideMap.get(def.command)
    if (override) {
      usedOverrideCommands.add(def.command)
      // Empty key = unbind
      if (override.key === '')
        continue
      result.push({
        key: override.key,
        command: def.command,
        when: override.when ?? def.when,
        args: def.args,
      })
    }
    else {
      result.push({ ...def })
    }
  }

  // Append overrides for commands not in defaults (new bindings)
  for (const o of overrides) {
    if (!usedOverrideCommands.has(o.command)) {
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

// ---------------------------------------------------------------------------
// Binding groups
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Resolver
// ---------------------------------------------------------------------------

const MODIFIER_RE = /\$mod|Control|Alt|Meta|Shift/

/** Check if a key string contains modifier keys. */
function hasModifier(key: string): boolean {
  const first = key.split(' ')[0]
  return MODIFIER_RE.test(first)
}

/** Check if focus is on an interactive input element. */
function isInputFocused(): boolean {
  const el = document.activeElement
  if (!el)
    return false

  const tag = el.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT')
    return true

  if (el.getAttribute('contenteditable') === 'true')
    return true

  return false
}

/**
 * Resolve which binding to execute for a given key event.
 * Returns the command ID to execute, or null if no match.
 */
export function resolve(bindings: readonly Keybinding[], key: string): string | null {
  const inputFocused = isInputFocused()
  const modifier = hasModifier(key)

  for (const binding of bindings) {
    // For non-modifier shortcuts, suppress when input is focused
    // unless the when-clause explicitly depends on input focus
    if (!modifier && inputFocused && !binding.when?.includes('inputFocused'))
      continue

    if (evaluateWhen(binding.when))
      return binding.command
  }

  return null
}

// ---------------------------------------------------------------------------
// Tinykeys integration
// ---------------------------------------------------------------------------

let currentUnsubscribe: (() => void) | null = null

/**
 * Bind all keybindings via tinykeys.
 * Call this when the binding table changes (init, user override change).
 */
export function bindAll(bindings: readonly Keybinding[]): void {
  unbindAll()

  const groups = groupBindings(bindings)
  const keyMap: Record<string, (e: KeyboardEvent) => void> = {}

  for (const group of groups) {
    keyMap[group.key] = (e: KeyboardEvent) => {
      const commandId = resolve(group.bindings, group.key)
      if (commandId) {
        e.preventDefault()
        executeCommand(commandId)
      }
    }
  }

  currentUnsubscribe = tinykeys(window, keyMap)
  log.debug(`Bound ${groups.length} key groups (${bindings.length} bindings)`)
}

/** Unbind all current keybindings. */
export function unbindAll(): void {
  currentUnsubscribe?.()
  currentUnsubscribe = null
}

// ---------------------------------------------------------------------------
// Binding lookup (for display helpers)
// ---------------------------------------------------------------------------

let activeBindings: readonly Keybinding[] = []

/** Update the active bindings reference (called by bindAll internally and useShortcuts). */
export function setActiveBindings(bindings: readonly Keybinding[]): void {
  activeBindings = bindings
}

/** Get the key string for a command ID (for displaying in tooltips). */
export function getBindingForCommand(commandId: string): string | undefined {
  for (const b of activeBindings) {
    if (b.command === commandId) {
      // Evaluate the when-clause to find the applicable binding
      if (evaluateWhen(b.when, getContext))
        return b.key
    }
  }
  // Fallback: return first binding for command regardless of context
  return activeBindings.find(b => b.command === commandId)?.key
}
