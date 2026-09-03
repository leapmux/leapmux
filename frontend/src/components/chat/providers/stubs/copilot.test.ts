import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { describeACPStubBasics } from './stubBasics'

import './copilot'

const MODE_AGENT = 'https://agentclientprotocol.com/protocol/session-modes#agent'
const MODE_PLAN = 'https://agentclientprotocol.com/protocol/session-modes#plan'

describe('copilot provider', () => {
  const plugin = providerFor(AgentProvider.GITHUB_COPILOT)!

  describeACPStubBasics(plugin, { text: true, image: true, pdf: true, binary: true })

  it('maps plan mode to the ACP URI value', () => {
    expect(plugin.planMode?.currentMode({ optionValues: { permissionMode: MODE_PLAN } })).toBe(MODE_PLAN)
    expect(plugin.planMode?.currentMode({ optionValues: { permissionMode: '' } })).toBe(MODE_AGENT)
  })

  it('maps smart and bypass permissions to the two permission axes', () => {
    expect(plugin.permissionPresets).toEqual({
      smart: { sets: { copilot_assisted_approval: 'on' } },
      bypass: { sets: { allow_all: 'on' } },
    })
  })

  it('declares plan mode on the permissionMode group and defaults to agent', () => {
    // The generic settings panel renders the permissionMode group Copilot reports;
    // the provider only declares the plan-mode mapping and its default mode.
    expect(plugin.planMode).toMatchObject({
      groupKey: 'permissionMode',
      planValue: MODE_PLAN,
      defaultValue: MODE_AGENT,
    })
  })

  it('renders the permissionMode group as the trigger mode segment', () => {
    expect(plugin.triggerModeGroupKey).toBe('permissionMode')
  })
})
