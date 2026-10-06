import type { Page } from '@playwright/test'
import type { SettingFeature } from './unsupportedConfiguration'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectMissingSetting, SETTING_GROUP_NAMES, settingGroupMatches, settingNameKey } from './unsupportedConfiguration'

/** The live state that the fakes of the browser and the Worker serve, one entry for each read. */
const live = vi.hoisted(() => ({
  events: [] as string[],
  catalogs: [] as { id: string, label: string }[][],
  menus: [] as { id: string, label: string }[][],
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return { ...actual, expect }
})

vi.mock('./nativeScenario', () => ({
  currentNativeAgent: async () => {
    live.events.push('catalog')
    const optionGroups = live.catalogs.shift()
    if (!optionGroups)
      throw new Error('The test supplied no catalog for this read.')
    return { status: AgentStatus.ACTIVE, optionGroups }
  },
}))

vi.mock('./ui', () => ({
  waitForNativeSettingsHydrated: async () => {
    live.events.push('hydrated')
  },
  openPlusMenu: async () => {
    live.events.push('menu')
    const groups = live.menus.shift()
    if (!groups)
      throw new Error('The test supplied no menu for this read.')
    return {
      locator: () => ({
        evaluateAll: async (read: (rows: Element[]) => unknown) => read(groups.map(group => ({
          getAttribute: (name: string) => name === 'data-testid' ? `composer-group-${group.id}` : null,
          textContent: ` ${group.label} `,
        }) as unknown as Element)),
      }),
    }
  },
  closeComposerMenus: async () => {
    live.events.push('closed')
  },
  expectPermissionShortcuts: vi.fn(),
}))

function fakeContext() {
  const page = {
    reload: async () => {
      live.events.push('reload')
    },
  } as unknown as Page
  return { page, modelScript: {} as never, provider: AgentProvider.PI, leapmuxServer: { hubUrl: '', adminToken: '', workerId: '' }, workspaceId: 'workspace' }
}

const MODEL_ONLY = [{ id: 'model', label: 'Model' }]

beforeEach(() => {
  live.events = []
  live.catalogs = []
  live.menus = []
})

describe('SETTING_GROUP_NAMES', () => {
  const features = Object.keys(SETTING_GROUP_NAMES) as SettingFeature[]

  it('gives each feature at least one name', () => {
    for (const feature of features)
      expect(SETTING_GROUP_NAMES[feature].length, feature).toBeGreaterThan(0)
  })

  it('gives each comparison key to one entry of one feature only', () => {
    const owners = new Map<string, string>()
    for (const feature of features) {
      for (const name of SETTING_GROUP_NAMES[feature]) {
        const key = settingNameKey(name)
        expect(key, `${feature} ${name}`).not.toBe('')
        expect(owners.get(key), `the key ${key} of ${feature} ${name}`).toBeUndefined()
        owners.set(key, `${feature} ${name}`)
      }
    }
  })

  it('uses a feature ID of the feature matrix for each key', () => {
    // Node refuses the URL object that `new URL(path, import.meta.url)` builds in the jsdom project ("The URL must be
    // of scheme file"), so the test reads a file path.
    const matrix = JSON.parse(readFileSync(join(import.meta.dirname, '../feature-matrix/features.json'), 'utf8')) as { features: { id: string }[] }
    const ids = new Set(matrix.features.map(feature => feature.id))
    for (const feature of features)
      expect(ids.has(feature), feature).toBe(true)
  })
})

describe('settingNameKey', () => {
  it.each(['fastMode', 'fast_mode', 'Fast Mode', 'FAST-MODE'])('gives %j the key of every other spelling', (name) => {
    expect(settingNameKey(name)).toBe('fastmode')
  })
})

describe('settingGroupMatches', () => {
  it('finds a group whose ID spells a known name in another form', () => {
    expect(settingGroupMatches('swarm-mode', [{ id: 'swarm_mode', label: 'Agents' }])).toEqual(['swarm_mode ("Agents")'])
  })

  it('finds a group that a native agent passes through with an unknown ID and a known label', () => {
    expect(settingGroupMatches('fast-mode', [{ id: 'turbo', label: 'Fast Mode' }])).toEqual(['turbo ("Fast Mode")'])
  })

  it('finds the contract ID of a provider', () => {
    expect(settingGroupMatches('fast-mode', [{ id: 'service_tier', label: 'Tier' }])).toEqual(['service_tier ("Tier")'])
  })

  it('keeps a group whose name only holds a known name as a part', () => {
    expect(settingGroupMatches('extended-thinking', [{ id: 'effort', label: 'Thinking Level' }, { id: 'thinking_effort', label: 'Thinking Effort' }])).toEqual([])
  })

  it('finds nothing in an empty catalog', () => {
    expect(settingGroupMatches('model', [])).toEqual([])
  })

  it('refuses a feature that the feature matrix does not hold', () => {
    // @ts-expect-error `swarm_mode` is an option ID, not a feature ID.
    expect(() => settingGroupMatches('swarm_mode', [])).toThrow('"swarm_mode" is not a settings feature')
  })
})

describe('expectMissingSetting', () => {
  it('runs the related proof first, then reads the catalog and the menu before and after a reload', async () => {
    live.catalogs = [MODEL_ONLY, MODEL_ONLY]
    live.menus = [MODEL_ONLY, MODEL_ONLY]
    await expectMissingSetting(fakeContext(), { feature: 'swarm-mode', relatedProof: async () => {
      live.events.push('proof')
    } })
    expect(live.events).toEqual(['proof', 'hydrated', 'catalog', 'menu', 'closed', 'reload', 'hydrated', 'catalog', 'menu', 'closed'])
  })

  it('fails for a catalog group that spells the feature in another form', async () => {
    live.catalogs = [[...MODEL_ONLY, { id: 'fast_mode', label: 'Speed' }]]
    live.menus = [MODEL_ONLY]
    await expect(expectMissingSetting(fakeContext(), { feature: 'fast-mode', relatedProof: async () => {} }))
      .rejects
      .toThrow('fast_mode ("Speed")')
  })

  it('fails for a menu group whose label states the feature', async () => {
    live.catalogs = [MODEL_ONLY]
    live.menus = [[...MODEL_ONLY, { id: 'style', label: 'Output Style' }]]
    await expect(expectMissingSetting(fakeContext(), { feature: 'output-style', relatedProof: async () => {} }))
      .rejects
      .toThrow('style ("Output Style")')
  })

  it('fails for a group that appears only after the reload', async () => {
    live.catalogs = [MODEL_ONLY, [...MODEL_ONLY, { id: 'alwaysThinkingEnabled', label: 'Extended Thinking' }]]
    live.menus = [MODEL_ONLY, MODEL_ONLY]
    await expect(expectMissingSetting(fakeContext(), { feature: 'extended-thinking', relatedProof: async () => {} }))
      .rejects
      .toThrow('after the reload')
  })

  it('fails for an empty catalog, which proves no live session', async () => {
    live.catalogs = [[]]
    await expect(expectMissingSetting(fakeContext(), { feature: 'model', relatedProof: async () => {} }))
      .rejects
      .toThrow('option groups before the reload')
  })

  it('reads nothing when the related proof fails', async () => {
    await expect(expectMissingSetting(fakeContext(), { feature: 'mode', relatedProof: async () => {
      throw new Error('the native operation failed')
    } })).rejects.toThrow('the native operation failed')
    expect(live.events).toEqual([])
  })
})
