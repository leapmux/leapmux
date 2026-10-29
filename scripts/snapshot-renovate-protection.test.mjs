import { describe, expect, it } from 'bun:test'
import { RENOVATE_MAIN_RULESET } from './check-renovate-protection.mjs'
import { generateProtectionSnapshot, readProtectionWithCli } from './snapshot-renovate-protection.mjs'

describe('administrator snapshot CLI', () => {
  it('reads only the intended repository through the existing CLI authentication', async () => {
    let command
    const data = await readProtectionWithCli('/repos/leapmux/leapmux/rulesets/2', async (...args) => {
      command = args
      return { stdout: '{"id":2}' }
    })
    expect(data).toEqual({ id: 2 })
    expect(command[0]).toBe('gh')
    expect(command[1]).toEqual([
      'api',
      '--hostname',
      'github.com',
      '--method',
      'GET',
      '-H',
      'X-GitHub-Api-Version: 2022-11-28',
      '-H',
      'Cache-Control: no-cache',
      '/repos/leapmux/leapmux/rulesets/2',
    ])
    expect(command[2].timeout).toBe(30000)
    expect(command[2]).not.toHaveProperty('shell')
    for (const path of [
      '/repos/other/repo/rulesets/2',
      '/repos/leapmux/leapmux/rulesets/0',
      '/repos/leapmux/leapmux/rules/branches/main?per_page=100&page=0',
      '/repos/leapmux/leapmux/rulesets/2/history',
      '/repos/leapmux/leapmux/actions/variables',
      '//api.github.com/',
      '/repos/leapmux/leapmux/rulesets/2; false',
    ]) {
      await expect(readProtectionWithCli(path, () => {
        throw new Error('Unexpected execution')
      })).rejects.toThrow('only read')
    }
    await expect(readProtectionWithCli('/repos/leapmux/leapmux/rulesets/2', async () => {
      throw new Error('CLI unavailable')
    })).rejects.toThrow('CLI unavailable')
    await expect(readProtectionWithCli('/repos/leapmux/leapmux/rulesets/2', async () => ({ stdout: 'not JSON' }))).rejects.toThrow()
  })
  it('validates all effective sources before capturing their actors', async () => {
    const baseline = {
      id: 1,
      enforcement: 'active',
      updated_at: '2026-10-28T00:00:00Z',
      bypass_actors: [],
    }
    const main = {
      ...structuredClone(RENOVATE_MAIN_RULESET),
      id: 2,
      updated_at: '2026-10-28T00:00:00Z',
    }
    const effective = ['deletion', 'non_fast_forward', 'required_linear_history'].map(type => ({ type, ruleset_id: 1 }))
    effective.push(...main.rules.map(rule => ({ ...rule, ruleset_id: 2 })))
    const paths = []
    const get = async (path) => {
      paths.push(path)
      if (path.includes('/branches/'))
        return effective
      return path.endsWith('/1')
        ? baseline
        : main
    }
    const snapshot = await generateProtectionSnapshot(get)
    expect(paths).toEqual(['/repos/leapmux/leapmux/rules/branches/main?per_page=100&page=1', '/repos/leapmux/leapmux/rulesets/1', '/repos/leapmux/leapmux/rulesets/2'])
    expect(snapshot.rulesets.map(rule => rule.id)).toEqual([1, 2])
    expect(snapshot.rulesets[1].bypass_actors).toEqual([
      { actor_type: 'RepositoryRole', actor_id: 5, bypass_mode: 'always' },
    ])
    delete baseline.bypass_actors
    await expect(generateProtectionSnapshot(get)).rejects.toThrow('omits bypass actors')
  })
})
