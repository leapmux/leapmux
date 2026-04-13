import type { Keybinding, UserKeybindingOverride } from './types'
import { tinykeys } from 'tinykeys'
import { createLogger } from '~/lib/logger'
import { executeCommand } from './commands'
import { evaluateWhen, getContext, whenReferencesKey } from './context'

const log = createLogger('shortcuts')

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
 */
export function mergeKeybindings(
  defaults: readonly Keybinding[],
  overrides: readonly UserKeybindingOverride[],
): Keybinding[] {
  const overrideMap = new Map<string, UserKeybindingOverride[]>()
  for (const o of overrides) {
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
    else {
      result.push({ ...def })
    }
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

/** Check if a key string contains modifier keys. */
function hasModifier(key: string): boolean {
  const first = key.split(' ')[0]
  return MODIFIER_RE.test(first)
}

/**
 * Resolve which binding to execute for a given key event.
 * Returns the command ID to execute, or null if no match.
 */
export function resolve(bindings: readonly Keybinding[], key: string): string | null {
  const inputFocused = !!getContext('inputFocused')
  const modifier = hasModifier(key)

  for (const binding of bindings) {
    // Non-modifier shortcuts are suppressed when input is focused,
    // unless the when-clause explicitly references inputFocused.
    if (!modifier && inputFocused && !whenReferencesKey(binding.when, 'inputFocused'))
      continue

    if (evaluateWhen(binding.when))
      return binding.command
  }

  return null
}

let currentUnsubscribe: (() => void) | null = null
let activeBindings: readonly Keybinding[] = []

/**
 * Activate keybindings: store them for tooltip lookup and bind via tinykeys.
 * Call this when the binding table changes (init, user override change).
 */
export function activateBindings(bindings: readonly Keybinding[]): void {
  unbindAll()
  activeBindings = bindings

  const groups = groupBindings(bindings)
  const keyMap: Record<string, (e: KeyboardEvent) => void> = {}

  for (const group of groups) {
    keyMap[group.key] = (e: KeyboardEvent) => {
      const commandId = resolve(group.bindings, group.key)
      if (commandId) {
        e.preventDefault()
        e.stopPropagation()
        executeCommand(commandId)
      }
    }
  }

  currentUnsubscribe = tinykeys(window, keyMap, { capture: true })
  log.debug(`Bound ${groups.length} key groups (${bindings.length} bindings)`)
}

/** Unbind all current keybindings. */
export function unbindAll(): void {
  currentUnsubscribe?.()
  currentUnsubscribe = null
}

/** Get the key string for a command ID (for displaying in tooltips). */
export function getBindingForCommand(commandId: string): string | undefined {
  let firstKey: string | undefined
  for (const b of activeBindings) {
    if (b.command === commandId) {
      if (evaluateWhen(b.when))
        return b.key
      firstKey ??= b.key
    }
  }
  return firstKey
}
