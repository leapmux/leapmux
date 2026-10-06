import type { Locator, Page } from '@playwright/test'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { COPILOT_MODE, COPILOT_OPTION, COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { COPILOT_PERMISSION_JUDGE_RULE, exerciseCopilotPresetSwitch } from './permissionScenario'

// The opening of each system prompt that Copilot 1.0.87 sent in Assisted mode.
const JUDGE_SYSTEM_PROMPT = '\nYou are Luna, a one-call permission judge. Decide whether the proposed action\nmay execute without further host handling. You have no tools. Protect users\nfrom serious harm and burdensome recovery while allowing ordinary work.\n'
const TURN_SYSTEM_PROMPT = 'You are GitHub Copilot, an AI coding agent built by GitHub. You are an interactive tool that helps users with software engineering tasks.\n\n# Tone and style\n'

/** The settings steps that the fakes record, in order, and the mode rows that the fake menu shows. */
const settings = vi.hoisted(() => ({ events: [] as string[], shownModes: [] as string[] }))

vi.mock('../helpers/ui', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/ui')>(),
  applyPermissionPreset: async (_page: unknown, kind: string) => { settings.events.push(`preset ${kind}`) },
  expectSettingsOptionChosen: async (_page: unknown, testId: string) => { settings.events.push(`chosen ${testId}`) },
  openSettingsMenu: async (_page: unknown, groupId: string) => {
    settings.events.push(`menu ${groupId}`)
    return { getByTestId: (testId: string) => modeRow(testId) }
  },
  closeComposerMenus: async () => { settings.events.push('close menus') },
}))

/** A fake row of the session mode menu. Its visibility check passes when the test lists the row as shown. */
function modeRow(testId: string): Locator {
  class FakeLocator {
    readonly _apiName = 'Locator'
    async _expect(expression: string) {
      settings.events.push(`${testId} ${expression}`)
      const shown = settings.shownModes.includes(testId)
      return { matches: shown, received: shown, log: [], timedOut: false }
    }
  }
  return new FakeLocator() as unknown as Locator
}

describe('COPILOT_PERMISSION_JUDGE_RULE', () => {
  const system = COPILOT_PERMISSION_JUDGE_RULE.when.system
  if (typeof system !== 'string')
    throw new Error('The judge rule states one pattern.')
  const pattern = new RegExp(system, 'i')

  it('selects the request of the permission judge', () => {
    expect(pattern.test(JUDGE_SYSTEM_PROMPT)).toBe(true)
  })

  it('leaves the turn of the agent to the ordered script', () => {
    expect(pattern.test(TURN_SYSTEM_PROMPT)).toBe(false)
  })

  it('answers with the one line that the judge asks for', () => {
    const text = COPILOT_PERMISSION_JUDGE_RULE.respond.text ?? ''
    expect(text).toMatch(/^(?:ALLOW|DENY): \S/)
    expect(text.split(':')[1]?.trim().split(/\s+/).length).toBeLessThanOrEqual(12)
  })
})

describe('exerciseCopilotPresetSwitch', () => {
  const planRow = `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`
  const autopilotRow = `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Autopilot}`

  beforeEach(() => {
    settings.events = []
    settings.shownModes = [planRow, autopilotRow]
  })

  it('requires Manual, then Allow All after Bypass, then Assisted after Smart, and keeps the Interactive session mode', async () => {
    await exerciseCopilotPresetSwitch({} as Page)
    expect(settings.events).toEqual([
      `chosen permissionMode-${COPILOT_PERMISSION_MODE.Manual}`,
      'preset bypass',
      `chosen permissionMode-${COPILOT_PERMISSION_MODE.AllowAll}`,
      'preset smart',
      `chosen permissionMode-${COPILOT_PERMISSION_MODE.Assisted}`,
      `chosen ${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Interactive}`,
      `menu ${COPILOT_OPTION.SessionMode}`,
      `${planRow} to.be.visible`,
      `${autopilotRow} to.be.visible`,
      'close menus',
    ])
  })

  it('fails when the session no longer offers the Autopilot mode', async () => {
    settings.shownModes = [planRow]
    await expect(exerciseCopilotPresetSwitch({} as Page)).rejects.toThrow(`the session offers the ${COPILOT_MODE.Autopilot} mode`)
  })
})
