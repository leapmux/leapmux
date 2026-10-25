import type { PersistedControlResponse } from '../persistedControlResponse'
import { describe, expect, it, vi } from 'vitest'
import { WORKSPACE_TRUST_CONTROL_TYPE, WORKSPACE_TRUST_DECISION } from '~/generated/contracts/workspace-trust'
import {
  withWorkspaceTrustControl,
  withWorkspaceTrustOptions,
  withWorkspaceTrustResponse,
  WORKSPACE_TRUST_OPTION,
  workspaceTrustPermission,
  workspaceTrustQuestion,
} from './workspaceTrust'

const QUESTION = { type: WORKSPACE_TRUST_CONTROL_TYPE.Question, path: '/work/project', configuration: ['MCP servers'] }

/** The bytes one sender call sent, decoded. */
function sentResponse(onRespond: ReturnType<typeof vi.fn>): unknown {
  const bytes = onRespond.mock.calls[0]?.[0] as Uint8Array
  return JSON.parse(new TextDecoder().decode(bytes))
}

/** A saved answer to `request`. */
function savedAnswer(request: Record<string, unknown>, decision: string): PersistedControlResponse {
  return {
    requestId: 'workspace-trust:session-1',
    claimToken: '',
    request,
    response: { type: 'control_response', response: { request_id: 'workspace-trust:session-1', response: { decision } } },
  }
}

describe('workspaceTrustQuestion', () => {
  it('reads the folder and what the trust controls', () => {
    expect(workspaceTrustQuestion(QUESTION)).toEqual({ path: '/work/project', configuration: ['MCP servers'] })
  })

  it('drops an empty and a non-string configuration word', () => {
    expect(workspaceTrustQuestion({ ...QUESTION, configuration: ['MCP servers', '', 7, null] })?.configuration).toEqual(['MCP servers'])
    expect(workspaceTrustQuestion({ ...QUESTION, configuration: 'MCP servers' })?.configuration).toEqual([])
  })

  it('reads no question from another payload', () => {
    expect(workspaceTrustQuestion({ type: 'permission' })).toBeUndefined()
    expect(workspaceTrustQuestion({ method: 'session.event' })).toBeUndefined()
    expect(workspaceTrustQuestion(undefined)).toBeUndefined()
  })
})

describe('workspaceTrustPermission', () => {
  it('states the folder, the agent and what the trust controls', () => {
    expect(workspaceTrustPermission({ path: '/work/project', configuration: ['MCP servers'] }, 'GitHub Copilot')).toEqual({
      kind: 'permission',
      permission: {
        title: 'Trust the folder /work/project',
        reason: 'GitHub Copilot loads the MCP servers of a project only from a folder that you trust, and it keeps your answer for the folder.',
        options: [
          { optionId: 'workspace-trust', kind: 'allow_once', name: 'Trust this folder' },
          { optionId: 'workspace-reject', kind: 'reject_once', name: 'Do not trust' },
        ],
      },
    })
  })

  it.each([
    [[], 'the configuration'],
    [['MCP servers', 'hooks'], 'the MCP servers and hooks'],
    [['MCP servers', 'hooks', 'agents'], 'the MCP servers, hooks and agents'],
  ])('joins the configuration words %j', (configuration, phrase) => {
    const permission = workspaceTrustPermission({ path: '/work/project', configuration }, 'Kimi Code')
    expect(permission.kind === 'permission' ? permission.permission.reason : '').toBe(`Kimi Code loads ${phrase} of a project only from a folder that you trust, and it keeps your answer for the folder.`)
  })

  it('titles a question without a folder', () => {
    const permission = workspaceTrustPermission({ path: '', configuration: [] }, 'GitHub Copilot')
    expect(permission.kind === 'permission' ? permission.permission.title : '').toBe('Trust this folder')
  })

  it('draws the options that an agent states for its own trust request', () => {
    const options = [{ optionId: 'trust', kind: 'allow_once', name: 'Trust this folder' }]
    const permission = workspaceTrustPermission({ path: '/work', configuration: [] }, 'Grok Build', options)
    expect(permission.kind === 'permission' ? permission.permission.options : []).toEqual(options)
  })
})

describe('withWorkspaceTrustControl', () => {
  it('draws the question, and hands every other payload to the plugin', () => {
    const extract = vi.fn(() => ({ kind: 'plan' as const, text: 'the plan' }))
    const wrapped = withWorkspaceTrustControl(extract, 'GitHub Copilot')
    expect(wrapped({ payload: QUESTION })).toMatchObject({ kind: 'permission', permission: { title: 'Trust the folder /work/project' } })
    expect(extract).not.toHaveBeenCalled()
    expect(wrapped({ payload: { method: 'session.event' } })).toEqual({ kind: 'plan', text: 'the plan' })
    expect(extract).toHaveBeenCalledTimes(1)
  })
})

describe('withWorkspaceTrustOptions', () => {
  it('sends Trust as an allow decision', async () => {
    const send = vi.fn(async () => {})
    const onRespond = vi.fn(async () => {})
    await withWorkspaceTrustOptions(send)(onRespond, 'workspace-trust:session-1', WORKSPACE_TRUST_OPTION.Trust)
    expect(sentResponse(onRespond)).toEqual({ type: 'control_response', response: { subtype: 'success', request_id: 'workspace-trust:session-1', response: { behavior: 'allow' } } })
    expect(send).not.toHaveBeenCalled()
  })

  it('sends Do not trust as a deny decision', async () => {
    const send = vi.fn(async () => {})
    const onRespond = vi.fn(async () => {})
    await withWorkspaceTrustOptions(send)(onRespond, 'workspace-trust:session-1', WORKSPACE_TRUST_OPTION.Reject)
    expect(sentResponse(onRespond)).toEqual({ type: 'control_response', response: { subtype: 'success', request_id: 'workspace-trust:session-1', response: { behavior: 'deny' } } })
    expect(send).not.toHaveBeenCalled()
  })

  it('hands every other option to the plugin', async () => {
    const send = vi.fn(async () => {})
    const onRespond = vi.fn(async () => {})
    await withWorkspaceTrustOptions(send)(onRespond, 'copilot-request', 'reject')
    expect(send).toHaveBeenCalledWith(onRespond, 'copilot-request', 'reject')
    expect(onRespond).not.toHaveBeenCalled()
  })
})

describe('withWorkspaceTrustResponse', () => {
  const display = vi.fn(() => ({ kind: 'label' as const, text: 'the plugin label' }))
  const wrapped = withWorkspaceTrustResponse(display)

  it('reads the two saved answers', () => {
    expect(wrapped(savedAnswer(QUESTION, WORKSPACE_TRUST_DECISION.Trust))).toEqual({ kind: 'label', text: 'Trusted this folder' })
    expect(wrapped(savedAnswer(QUESTION, WORKSPACE_TRUST_DECISION.Reject))).toEqual({ kind: 'label', text: 'Did not trust this folder' })
  })

  it('reads no label from an answer without a known decision', () => {
    expect(wrapped(savedAnswer(QUESTION, 'maybe'))).toBeNull()
    expect(wrapped({ ...savedAnswer(QUESTION, ''), response: undefined })).toBeNull()
  })

  it('hands the answer to another request to the plugin', () => {
    expect(wrapped(savedAnswer({ method: 'session.event' }, WORKSPACE_TRUST_DECISION.Trust))).toEqual({ kind: 'label', text: 'the plugin label' })
    expect(display).toHaveBeenCalledTimes(1)
  })
})
