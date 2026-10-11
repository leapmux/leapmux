import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { approvalPayload, questionPayload } from './controls.fixtures'
import { codewhaleExtractControl } from './extractControl'

describe('codewhaleExtractControl', () => {
  it('reads an approval of a command, with the command the call runs', () => {
    expect(codewhaleExtractControl({ payload: approvalPayload(CODEWHALE_TOOL.Bash, { command: 'touch x' }) })).toStrictEqual({
      kind: 'permission',
      permission: { title: CODEWHALE_TOOL.Bash, input: { command: 'touch x' }, command: 'touch x', options: [] },
    })
  })

  it('states the call\'s intent, never the tool\'s static description', () => {
    expect(codewhaleExtractControl({ payload: approvalPayload(CODEWHALE_TOOL.ApplyPatch, { patch: 'x' }, { intent_summary: 'Rename the helper' }) }))
      .toStrictEqual({ kind: 'permission', permission: { title: CODEWHALE_TOOL.ApplyPatch, reason: 'Rename the helper', input: { patch: 'x' }, options: [] } })
  })

  it('states the summary when the call gives no intent, and the intent when it gives both', () => {
    expect(codewhaleExtractControl({ payload: approvalPayload(CODEWHALE_TOOL.Write, { path: 'a' }, { summary: 'Write a' }) }))
      .toStrictEqual({ kind: 'permission', permission: { title: CODEWHALE_TOOL.Write, reason: 'Write a', input: { path: 'a' }, options: [] } })
    expect(codewhaleExtractControl({ payload: approvalPayload(CODEWHALE_TOOL.Write, { path: 'a' }, { intent_summary: 'Create the file', summary: 'Write a' }) }))
      .toMatchObject({ permission: { reason: 'Create the file' } })
    // Blank words are no reason, so the permission states none rather than an empty one.
    expect(codewhaleExtractControl({ payload: approvalPayload(CODEWHALE_TOOL.Write, { path: 'a' }, { intent_summary: '', summary: '' }) }))
      .toStrictEqual({ kind: 'permission', permission: { title: CODEWHALE_TOOL.Write, input: { path: 'a' }, options: [] } })
  })

  // The kind table decides which tools run a command. A command tool that states
  // what it runs in another argument, and a file tool that happens to carry a
  // `command` key, both draw their arguments alone.
  it('states a command only for a command tool that sends a command argument', () => {
    expect(codewhaleExtractControl({ payload: approvalPayload(CODEWHALE_TOOL.TerminalSend, { terminal_id: 't1', input: 'y\n' }) }))
      .toStrictEqual({ kind: 'permission', permission: { title: CODEWHALE_TOOL.TerminalSend, input: { terminal_id: 't1', input: 'y\n' }, options: [] } })
    expect(codewhaleExtractControl({ payload: approvalPayload(CODEWHALE_TOOL.Write, { path: 'a', command: 'rm -rf /' }) }))
      .toStrictEqual({ kind: 'permission', permission: { title: CODEWHALE_TOOL.Write, input: { path: 'a', command: 'rm -rf /' }, options: [] } })
    expect(codewhaleExtractControl({ payload: approvalPayload(CODEWHALE_TOOL.Bash, { command: '' }) }))
      .toStrictEqual({ kind: 'permission', permission: { title: CODEWHALE_TOOL.Bash, input: { command: '' }, options: [] } })
  })

  it('states an empty argument record for a header that carries no request', () => {
    const payload = { ...approvalPayload(CODEWHALE_TOOL.Bash, {}), request: undefined }
    expect(codewhaleExtractControl({ payload })).toStrictEqual({ kind: 'permission', permission: { title: CODEWHALE_TOOL.Bash, input: {}, options: [] } })
  })

  it('takes the tool name from the event when the header lost it', () => {
    const payload = { ...approvalPayload(CODEWHALE_TOOL.Bash, {}), request: { input: {} } }
    expect(codewhaleExtractControl({ payload })).toMatchObject({ permission: { title: CODEWHALE_TOOL.Bash } })
  })

  it('answers null for a question and for a payload with no event', () => {
    expect(codewhaleExtractControl({ payload: questionPayload() })).toBeNull()
    expect(codewhaleExtractControl({ payload: {} })).toBeNull()
    expect(codewhaleExtractControl({ payload: { ...approvalPayload(CODEWHALE_TOOL.Bash, {}), event: 'approval.required' } })).toBeNull()
  })
})
