import { COMMAND_CODE_FRAME_KIND } from '~/generated/contracts/commandcode-protocol'
import { isObject, pickString } from '~/lib/jsonPick'

/** Read a native event without claiming another provider envelope. */
export function commandCodeEvent(payload: unknown): Record<string, unknown> | undefined {
  return isObject(payload) && payload.type === COMMAND_CODE_FRAME_KIND.Event && isObject(payload.event)
    ? payload.event
    : undefined
}

/** Native text blocks retain their exact order and whitespace. */
export function commandCodeText(blocks: unknown): string {
  return Array.isArray(blocks)
    ? blocks.filter(isObject).filter(block => block.type === 'text').map(block => pickString(block, 'text')).join('')
    : ''
}

export function commandCodeError(error: unknown): string {
  return typeof error === 'string' ? error : isObject(error) ? pickString(error, 'message') : ''
}

export const COMMAND_CODE_TOOL_NAME = {
  ReadFile: 'read_file',
  WriteFile: 'write_file',
  EditFile: 'edit_file',
  ReadDirectory: 'read_directory',
  Glob: 'glob',
  Grep: 'grep',
  SearchTools: 'search_tools',
  TaskList: 'task_list',
  TaskGet: 'task_get',
  ShellTasks: 'shell_tasks',
  PowerShell: 'powershell',
  ActivateSkill: 'activate_skill',
  Sleep: 'sleep',
  WebSearch: 'web_search',
  WebFetch: 'web_fetch',
  RunCommand: 'run_command',
} as const
