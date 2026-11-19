import type { ChatRow } from './model/row'
import type { ControlDecision, ControlPrompt } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { create } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it } from 'vitest'
import { COPILOT_MODE, COPILOT_OPTION, COPILOT_PERMISSION_MODE } from '~/generated/contracts/copilot-protocol'
import { ControlDecisionSchema, ControlPromptSchema, ElicitationAction, PermissionOptionKind } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider, AvailableOptionGroupSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { clearSettingsLabelCache, collectSettingsLabelDependencies, updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { makeControlResponseMessage } from '~/test-support/messageFactory'
import { classifyMessage, toClassificationInput } from './messageClassifier'
import { resolveMessageForRendering } from './providers/registry'
import { extractChatRow, extractedRow } from './rowExtraction'
import { OPTION_ID_PERMISSION_MODE } from './settingsGroups'
import './providers'

/**
 * A saved answer row as the Worker writes it: the provider's own answer bytes as
 * the content, and the prompt and the decision in the Worker's metadata. The row
 * reads its words through the whole layer-1 path, classification and extraction, as
 * the transcript reads them.
 */
function savedAnswerRow(provider: AgentProvider, answer: unknown, prompt: ControlPrompt, decision: ControlDecision): ChatRow | null {
  const message = makeControlResponseMessage(provider, answer, undefined, { prompt, decision })
  const parsed = resolveMessageForRendering(parseMessageContent(message), provider)
  return extractedRow(extractChatRow(provider, parsed, classifyMessage(toClassificationInput(parsed, message))))
}

describe('saved answer rows', () => {
  // Codex's item/permissions/requestApproval answers with a grant, which states no
  // decision word, so a reader of the answer bytes found nothing and the row read
  // "Responded". The decision states the option the user chose.
  it('reads a Codex permissions grant as the option the user chose', () => {
    const prompt = create(ControlPromptSchema, {
      kind: {
        case: 'permission',
        value: {
          title: 'Permissions',
          options: [
            { optionId: 'turn', kind: PermissionOptionKind.ALLOW_ONCE, name: 'Allow' },
            { optionId: 'session', kind: PermissionOptionKind.ALLOW_ALWAYS, name: 'Allow for Session' },
            { optionId: 'deny', kind: PermissionOptionKind.REJECT_ONCE, name: 'Deny' },
          ],
        },
      },
    })
    const decision = create(ControlDecisionSchema, {
      kind: {
        case: 'permission',
        value: { optionId: 'session' },
      },
    })
    const answer = {
      jsonrpc: '2.0',
      id: 3,
      result: {
        permissions: {
          network: { enabled: true },
        },
        scope: 'session',
      },
    }
    expect(savedAnswerRow(AgentProvider.CODEX, answer, prompt, decision)).toEqual({
      kind: 'control-response',
      display: { kind: 'label', text: 'Allow for Session' },
    })
  })

  // ZCode answers an MCP form positionally (`answer_<i>`), so a reader of the answer
  // bytes found no field of the form's schema and drew the positional keys. The
  // decision states the form as the user filled it, keyed by the schema's fields.
  it('reads a ZCode form with the titles of its fields', () => {
    const prompt = create(ControlPromptSchema, {
      kind: {
        case: 'elicitation',
        value: {
          mode: 'form',
          message: 'Configure the deployment',
          schemaJson: JSON.stringify({
            type: 'object',
            properties: {
              replicas: { type: 'integer', title: 'Replica count' },
              region: {
                type: 'string',
                title: 'Region',
                enum: ['us', 'eu'],
                enumNames: ['United States', 'Europe'],
              },
            },
          }),
        },
      },
    })
    const decision = create(ControlDecisionSchema, {
      kind: {
        case: 'elicitation',
        value: {
          action: ElicitationAction.ACCEPT,
          contentJson: JSON.stringify({ replicas: 0, region: 'eu' }),
        },
      },
    })
    const answer = {
      id: 'server-4',
      result: {
        action: 'accept',
        content: { answer_0: '0', answer_1: 'eu' },
      },
    }
    expect(savedAnswerRow(AgentProvider.ZCODE, answer, prompt, decision)).toEqual({
      kind: 'control-response',
      display: { kind: 'label', text: 'Approved\nReplica count: 0\nRegion: Europe' },
    })
  })

  describe('a plan approval that switches the permission mode', () => {
    afterEach(() => clearSettingsLabelCache())

    const prompt = create(ControlPromptSchema, {
      kind: { case: 'plan', value: {} },
    })
    const decision = create(ControlDecisionSchema, {
      kind: {
        case: 'plan',
        value: { approve: true, permissionMode: 'act' },
      },
    })
    const answer = { approvalId: 'approval_1', approved: true, permissionMode: 'act' }

    // Cline's approval of a plan switches to Act or to Auto-approve, and the row names
    // the mode in Cline's own words, as the agent's settings state them.
    it('reads the mode in the words of the agent\'s settings', () => {
      updateSettingsLabelCache(AgentProvider.CLINE, [
        create(AvailableOptionGroupSchema, {
          id: 'permissionMode',
          options: [
            { id: 'plan', name: 'Plan' },
            { id: 'act', name: 'Act' },
          ],
        }),
      ])
      expect(savedAnswerRow(AgentProvider.CLINE, answer, prompt, decision)).toEqual({
        kind: 'control-response',
        display: { kind: 'label', text: 'Approve (Act)' },
      })
    })

    // Copilot keeps two mode axes: its session mode (Interactive, Plan, Autopilot), which the
    // mode chip reads, and its permission mode, which a plan approval switches. The row names
    // the permission mode, so it reads the permission-mode group and never the session-mode one.
    it('reads a Copilot mode from its permission-mode group', () => {
      updateSettingsLabelCache(AgentProvider.GITHUB_COPILOT, [
        create(AvailableOptionGroupSchema, {
          id: COPILOT_OPTION.SessionMode,
          options: [
            { id: COPILOT_MODE.Interactive, name: 'Interactive' },
            { id: COPILOT_MODE.Plan, name: 'Plan' },
            { id: COPILOT_MODE.Autopilot, name: 'Autopilot' },
          ],
        }),
        create(AvailableOptionGroupSchema, {
          id: OPTION_ID_PERMISSION_MODE,
          options: [
            { id: COPILOT_PERMISSION_MODE.Manual, name: 'Manual' },
            { id: COPILOT_PERMISSION_MODE.Assisted, name: 'Assisted' },
            { id: COPILOT_PERMISSION_MODE.AllowAll, name: 'Allow All' },
          ],
        }),
      ])
      const approval = create(ControlDecisionSchema, {
        kind: {
          case: 'plan',
          value: { approve: true, permissionMode: COPILOT_PERMISSION_MODE.Assisted },
        },
      })
      expect(savedAnswerRow(AgentProvider.GITHUB_COPILOT, { approved: true }, prompt, approval)).toEqual({
        kind: 'control-response',
        display: { kind: 'label', text: 'Approve (Assisted)' },
      })
    })

    it('reads the mode by its value while the browser holds no settings of the agent', () => {
      expect(savedAnswerRow(AgentProvider.CLINE, answer, prompt, decision)).toEqual({
        kind: 'control-response',
        display: { kind: 'label', text: 'Approve (act)' },
      })
    })

    // The transcript caches a row until a label that it read changes, so the row must
    // read the label through the cache that records what it read.
    it('records the label group that it read, so the row redraws when the labels arrive', () => {
      const read = collectSettingsLabelDependencies(() => savedAnswerRow(AgentProvider.CLINE, answer, prompt, decision))
      expect(read.dependencies).toEqual([
        { provider: AgentProvider.CLINE, groupId: 'permissionMode' },
      ])
    })
  })
})
