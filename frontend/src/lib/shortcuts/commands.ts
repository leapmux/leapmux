import type { Command } from './types'
import { createLogger } from '~/lib/logger'

const log = createLogger('shortcuts')

const commands = new Map<string, Command>()

/**
 * Register a command. Returns an unregister function.
 * If a command with the same ID already exists, it is replaced.
 */
export function registerCommand(cmd: Command): () => void {
  commands.set(cmd.id, cmd)
  return () => {
    // Only delete if it's still the same command instance
    if (commands.get(cmd.id) === cmd)
      commands.delete(cmd.id)
  }
}

export function unregisterCommand(id: string): void {
  commands.delete(id)
}

export function executeCommand(id: string): void {
  const cmd = commands.get(id)
  if (!cmd) {
    log.warn(`Unknown command: ${id}`)
    return
  }
  try {
    const result = cmd.handler()
    if (result instanceof Promise)
      result.catch(err => log.warn(`Command ${id} failed`, err))
  }
  catch (err) {
    log.warn(`Command ${id} threw`, err)
  }
}

export function getCommand(id: string): Command | undefined {
  return commands.get(id)
}

export function getAllCommands(): Command[] {
  return [...commands.values()]
}

/** Clear all commands — mainly useful for tests. */
export function resetCommands(): void {
  commands.clear()
}
