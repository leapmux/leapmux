import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ohMyPiExtractControl } from './extractControl'

const options = [
  { optionId: 'Approve', kind: 'allow_once', name: 'Approve' },
  { optionId: 'Deny', kind: 'reject_once', name: 'Deny' },
]

function approval(title: string): Record<string, unknown> {
  return { type: 'extension_ui_request', id: 'a1', method: 'select', title, options: ['Approve', 'Deny'] }
}

function source(parentObject: Record<string, unknown>): ParsedMessageContent {
  return { wrapper: null, topLevel: parentObject, parentObject, rawText: '', supplementalContent: undefined, messageMetadata: undefined }
}

describe('ohMyPiExtractControl', () => {
  it('reads omp\'s tool approval as a permission with its two answers', () => {
    // omp 18.2.11's own dialog (probe s2).
    expect(ohMyPiExtractControl({ payload: approval('Allow tool: bash\nCommand: echo approved-run') })).toEqual({
      kind: 'permission',
      permission: { title: 'bash', command: 'echo approved-run', options },
    })
  })

  it('keeps a command that spans several lines whole', () => {
    const control = ohMyPiExtractControl({ payload: approval('Allow tool: bash\nReason: Prompt required by bash pattern: rm\nCommand: printf a\nprintf b\nProvider safety checks:\n- sandbox off') })
    expect(control?.kind === 'permission' ? control.permission : null).toEqual({
      title: 'bash',
      reason: 'Reason: Prompt required by bash pattern: rm\n\nProvider safety checks:\n- sandbox off',
      command: 'printf a\nprintf b',
      options,
    })
  })

  it('reads the arguments from the call\'s own start frame', () => {
    const control = ohMyPiExtractControl({
      payload: approval('Allow tool: bash\nCommand: echo shor[…10ch elided…]'),
      source: source({ type: 'tool_execution_start', toolCallId: 'call_4', toolName: 'bash', args: { command: 'echo short and complete' } }),
    })
    expect(control?.kind === 'permission' ? control.permission : null).toEqual({
      title: 'bash',
      command: 'echo short and complete',
      input: { command: 'echo short and complete' },
      options,
    })
  })

  it('ignores a start frame of another tool', () => {
    const control = ohMyPiExtractControl({
      payload: approval('Allow tool: bash\nCommand: ls'),
      source: source({ type: 'tool_execution_start', toolCallId: 'call_4', toolName: 'read', args: { path: 'a' } }),
    })
    expect(control?.kind === 'permission' ? control.permission.input : 'none').toBeUndefined()
  })

  it('ignores a source frame that is not the call\'s start frame', () => {
    // An end frame of the same tool states the call's result, not its arguments.
    const control = ohMyPiExtractControl({
      payload: approval('Allow tool: bash\nCommand: ls'),
      source: source({ type: 'tool_execution_end', toolCallId: 'call_4', toolName: 'bash', args: { command: 'rm -rf /' }, result: {} }),
    })
    expect(control?.kind === 'permission' ? control.permission : null).toEqual({ title: 'bash', command: 'ls', options })
  })

  it('states the arguments of a call that runs no command, and keeps the lines of its title', () => {
    const control = ohMyPiExtractControl({
      payload: approval('Allow tool: write\nPath: notes.txt'),
      source: source({ type: 'tool_execution_start', toolCallId: 'call_5', toolName: 'write', args: { path: 'notes.txt', content: 'x' } }),
    })
    expect(control?.kind === 'permission' ? control.permission : null).toEqual({
      title: 'write',
      reason: 'Path: notes.txt',
      input: { path: 'notes.txt', content: 'x' },
      options,
    })
  })

  it('reads the command from the title when the start frame states no arguments', () => {
    const control = ohMyPiExtractControl({
      payload: approval('Allow tool: bash\nCommand: ls'),
      source: source({ type: 'tool_execution_start', toolCallId: 'call_4', toolName: 'bash' }),
    })
    expect(control?.kind === 'permission' ? control.permission : null).toEqual({ title: 'bash', command: 'ls', options })
  })

  it('states no tool for an approval whose title is the prefix alone', () => {
    expect(ohMyPiExtractControl({ payload: approval('Allow tool: ') })).toEqual({ kind: 'permission', permission: { options } })
  })

  it('reads a command with no safety checks and details before it', () => {
    // The command is the LAST detail, so the rows above it are the reason.
    const control = ohMyPiExtractControl({ payload: approval('Allow tool: bash\nOrigin: agent\nCommand: git push --force') })
    expect(control?.kind === 'permission' ? control.permission : null).toEqual({ title: 'bash', reason: 'Origin: agent', command: 'git push --force', options })
  })

  it('keeps the lines of an approval with no command', () => {
    const control = ohMyPiExtractControl({ payload: approval('Allow tool: mcp__github_create_issue\nOrigin: MCP server tool\nReason: writes to GitHub') })
    expect(control?.kind === 'permission' ? control.permission : null).toEqual({
      title: 'mcp__github_create_issue',
      reason: 'Origin: MCP server tool\nReason: writes to GitHub',
      options,
    })
    expect(ohMyPiExtractControl({ payload: approval('Allow tool: write') })).toEqual({ kind: 'permission', permission: { title: 'write', options } })
  })

  // A confirm, an input and an editor that an extension raises draw as a dialog, with
  // the fields omp states for each: the message, the hint, the draft and the deadline.
  it('reads an extension\'s confirm, input and editor as a dialog', () => {
    const dialog = (extra: Record<string, unknown>) => ohMyPiExtractControl({ payload: { type: 'extension_ui_request', id: 'd1', ...extra } })
    expect(dialog({ method: 'confirm', title: 'Proceed?', message: 'This deletes the branch.', timeout: 30000 })).toEqual({
      kind: 'dialog',
      dialog: { title: 'Proceed?', message: 'This deletes the branch.', variant: 'confirm', timeoutMs: 30000 },
    })
    expect(dialog({ method: 'input', title: 'Branch name', placeholder: 'main' })).toEqual({
      kind: 'dialog',
      dialog: { title: 'Branch name', placeholder: 'main', variant: 'input' },
    })
    expect(dialog({ method: 'editor', title: 'Commit message', prefill: 'fix: ' })).toEqual({
      kind: 'dialog',
      dialog: { title: 'Commit message', prefill: 'fix: ', variant: 'editor' },
    })
  })

  it('states no deadline for a dialog that waits with no limit', () => {
    for (const timeout of [undefined, 0, -1, 'soon']) {
      const control = ohMyPiExtractControl({ payload: { type: 'extension_ui_request', id: 'd1', method: 'confirm', title: 'Proceed?', timeout } })
      expect(control?.kind === 'dialog' ? control.dialog.timeoutMs : 'none', String(timeout)).toBeUndefined()
    }
  })

  it('keeps an empty message, hint and draft, which omp states on purpose', () => {
    const control = ohMyPiExtractControl({ payload: { type: 'extension_ui_request', id: 'd1', method: 'editor', title: 'Commit message', message: '', placeholder: '', prefill: '' } })
    expect(control).toEqual({ kind: 'dialog', dialog: { title: 'Commit message', message: '', placeholder: '', prefill: '', variant: 'editor' } })
  })

  it('leaves out a message, a hint and a draft that are not text', () => {
    const control = ohMyPiExtractControl({ payload: { type: 'extension_ui_request', id: 'd1', method: 'input', title: 'Name', message: 3, placeholder: null, prefill: ['x'] } })
    expect(control).toEqual({ kind: 'dialog', dialog: { title: 'Name', variant: 'input' } })
  })

  it('answers null for a method that `Object.prototype` holds', () => {
    // An extension picks the method word; a plain lookup would answer these from the
    // prototype and draw a dialog with no variant.
    for (const method of ['constructor', 'toString', 'hasOwnProperty', '__proto__'])
      expect(ohMyPiExtractControl({ payload: { type: 'extension_ui_request', id: 'd1', method } }), method).toBeNull()
  })

  it('gives a dialog with no title a heading of its own', () => {
    const titles = ['confirm', 'input', 'editor'].map((method) => {
      const control = ohMyPiExtractControl({ payload: { type: 'extension_ui_request', id: 'd1', method } })
      return control?.kind === 'dialog' ? control.dialog.title : null
    })
    expect(titles).toEqual(['Confirm', 'Enter a value', 'Enter your response'])
  })

  // A select other than the approval is a question, which the question form reads.
  it('answers null for a select, a question request and anything else', () => {
    expect(ohMyPiExtractControl({ payload: { type: 'extension_ui_request', id: 'x', method: 'select', title: 'Pick one', options: ['a', 'b'] } })).toBeNull()
    expect(ohMyPiExtractControl({ payload: { type: 'extension_ui_request', id: 'x', method: 'notify', message: 'Done.' } })).toBeNull()
    expect(ohMyPiExtractControl({ payload: { type: 'response', id: 'x', method: 'confirm' } })).toBeNull()
    expect(ohMyPiExtractControl({ payload: { type: 'leapmux_ask', id: 'q', questions: [] } })).toBeNull()
    expect(ohMyPiExtractControl({ payload: {} })).toBeNull()
  })
})
