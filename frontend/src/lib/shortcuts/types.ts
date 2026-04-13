export type Platform = 'mac' | 'windows' | 'linux'
export type ContextValue = string | boolean | number | undefined

export interface Command {
  id: string
  title: string
  category?: string
  handler: () => void | Promise<void>
}

export interface Keybinding {
  key: string
  command: string
  when?: string
  args?: unknown
}

export interface UserKeybindingOverride {
  key: string
  command: string
  when?: string
}
