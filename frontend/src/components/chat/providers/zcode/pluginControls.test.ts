import { describe, expect, it, vi } from 'vitest'
import { WORKSPACE_TRUST_CONTROL_TYPE, WORKSPACE_TRUST_DECISION } from '~/generated/contracts/workspace-trust'
import { ZCODE_METHOD, ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { WORKSPACE_TRUST_OPTION, WORKSPACE_TRUST_OPTIONS } from '../workspaceTrust'
import { zcodeControls } from './pluginControls'

function decode(bytes: unknown): Record<string, unknown> {
  return JSON.parse(new TextDecoder().decode(bytes as Uint8Array))
}

// The Worker asks ZCode's hook-trust question itself, in LeapMux's neutral shape.
describe('zcodeControls', () => {
  const question = { type: WORKSPACE_TRUST_CONTROL_TYPE.Question, path: '/work/project', configuration: ['hooks'] }
  const permission = {
    method: ZCODE_METHOD.RequestPermission,
    request: { tool_name: ZCODE_TOOL.Bash, input: { command: 'ls' } },
    params: { options: [{ optionId: 'allow', name: 'Allow' }, { optionId: 'deny', name: 'Deny' }] },
  }

  it('draws the hook-trust question with the name of the agent', () => {
    expect(zcodeControls.extractControl?.({ payload: question })).toEqual({
      kind: 'permission',
      permission: {
        title: 'Trust the folder /work/project',
        reason: 'ZCode loads the hooks of a project only from a folder that you trust, and it keeps your answer for the folder.',
        options: [...WORKSPACE_TRUST_OPTIONS],
      },
    })
  })

  it('still draws a native permission request', () => {
    expect(zcodeControls.extractControl?.({ payload: permission })).toMatchObject({ kind: 'permission' })
  })

  it.each([
    [WORKSPACE_TRUST_OPTION.Trust, 'allow'],
    [WORKSPACE_TRUST_OPTION.Reject, 'deny'],
  ])('sends the trust option %s as the neutral %s decision', async (optionId, behavior) => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    await zcodeControls.sendPermissionOption?.(onRespond, 'workspace-trust:session-1', optionId)
    expect(decode(onRespond.mock.calls[0]?.[0])).toEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'workspace-trust:session-1', response: { behavior } },
    })
  })

  it('still sends a native option with its choice', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    await zcodeControls.sendPermissionOption?.(onRespond, 'req-1', 'allow_once')
    expect(decode(onRespond.mock.calls[0]?.[0])).toMatchObject({
      response: { request_id: 'req-1', response: { behavior: 'allow' } },
    })
  })

  it.each([
    [WORKSPACE_TRUST_DECISION.Trust, 'Trusted this folder'],
    [WORKSPACE_TRUST_DECISION.Reject, 'Did not trust this folder'],
  ])('reads the saved answer %s', (decision, text) => {
    expect(zcodeControls.controlResponseDisplay?.({
      requestId: 'workspace-trust:session-1',
      claimToken: '',
      request: question,
      response: { type: 'control_response', response: { request_id: 'workspace-trust:session-1', response: { decision } } },
    })).toEqual({ kind: 'label', text })
  })
})
