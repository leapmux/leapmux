import { describe, expect, it } from 'bun:test'
import { checkProtection, RENOVATE_MAIN_RULESET, verifyRepository } from './check-renovate-protection.mjs'

function fixture() {
  const rulesets = [{ id: 1, enforcement: 'active', bypass_actors: [] }, {
    id: 2,
    enforcement: 'active',
    bypass_actors: [{ actor_type: 'RepositoryRole', actor_id: 5, bypass_mode: 'always' }],
  }]
  const effective = ['deletion', 'non_fast_forward', 'required_linear_history'].map(type => ({ type, ruleset_id: 1 }))
  effective.push({ type: 'pull_request', ruleset_id: 2, parameters: {
    required_approving_review_count: 1,
    dismiss_stale_reviews_on_push: true,
    require_last_push_approval: true,
  } }, { type: 'required_status_checks', ruleset_id: 2, parameters: {
    strict_required_status_checks_policy: true,
    required_status_checks: ['Required CI', 'NOTICE'].map(context => ({ context, integration_id: 15368 })),
  } })
  return { effective, rulesets }
}

describe('checkProtection', () => {
  it('rejects checks that permit stale base revisions', () => {
    for (const value of [false, undefined]) {
      const { effective, rulesets } = fixture()
      effective[4].parameters.strict_required_status_checks_policy = value
      expect(() => checkProtection(effective, rulesets)).toThrow('strict')
    }
  })
  it('requires administrator bypass on every review or check rule that can block a release push', () => {
    for (const type of ['pull_request', 'required_status_checks']) {
      const { effective, rulesets } = fixture()
      rulesets.push({ id: 3, enforcement: 'active', bypass_actors: [] })
      effective.push({ ...structuredClone(effective[type === 'pull_request' ? 3 : 4]), ruleset_id: 3 })
      expect(() => checkProtection(effective, rulesets), type).toThrow('administrator')
    }
    const { effective, rulesets } = fixture()
    rulesets.push({ id: 3, enforcement: 'active', bypass_actors: [] })
    effective[4].ruleset_id = 3
    expect(() => checkProtection(effective, rulesets)).toThrow('administrator')
    rulesets[2].bypass_actors.push({ actor_type: 'RepositoryRole', actor_id: 5, bypass_mode: 'always' })
    expect(() => checkProtection(effective, rulesets)).not.toThrow()
  })
  it('accepts separate effective rulesets with only an administrator bypass', () => {
    const { effective, rulesets } = fixture()
    expect(() => checkProtection(effective, rulesets)).not.toThrow()
  })
  it('accepts the emitted ruleset alongside existing protection and stricter reviews', () => {
    const baseline = fixture()
    const emitted = { ...structuredClone(RENOVATE_MAIN_RULESET), id: 3 }
    const effective = baseline.effective.slice(0, 3).concat(emitted.rules.map(rule => ({ ...rule, ruleset_id: 3 })))
    expect(() => checkProtection(effective, [baseline.rulesets[0], emitted])).not.toThrow()
    effective.find(rule => rule.type === 'pull_request').parameters.required_approving_review_count = 2
    expect(() => checkProtection(effective, [baseline.rulesets[0], emitted])).not.toThrow()
  })
  it('rejects each missing protection and unverifiable ruleset', () => {
    for (const index of [0, 1, 2, 3, 4]) {
      const { effective, rulesets } = fixture()
      effective.splice(index, 1)
      expect(() => checkProtection(effective, rulesets)).toThrow()
    }
    for (const rulesets of [[], [{ id: 1, enforcement: 'disabled', bypass_actors: [] }]])
      expect(() => checkProtection(fixture().effective, rulesets)).toThrow()
  })
  it('rejects weakened reviews, spoofed checks, and app or organization bypasses', () => {
    for (const [key, value] of [['required_approving_review_count', 0], ['dismiss_stale_reviews_on_push', false], ['require_last_push_approval', false]]) {
      const { effective, rulesets } = fixture()
      effective[3].parameters[key] = value
      expect(() => checkProtection(effective, rulesets)).toThrow()
    }
    for (const integration_id of [null, 0, 99]) {
      const { effective, rulesets } = fixture()
      effective[4].parameters.required_status_checks[0].integration_id = integration_id
      expect(() => checkProtection(effective, rulesets)).toThrow()
    }
    for (const actor of [{ actor_type: 'Integration', actor_id: 1, bypass_mode: 'always' }, { actor_type: 'OrganizationAdmin', actor_id: 1, bypass_mode: 'always' }, { actor_type: 'RepositoryRole', actor_id: 4, bypass_mode: 'always' }]) {
      const { effective, rulesets } = fixture()
      rulesets[0].bypass_actors.push(actor)
      expect(() => checkProtection(effective, rulesets)).toThrow()
    }
  })
  it('reads all effective sources and refuses failed API reads without writes', async () => {
    const { effective, rulesets } = fixture()
    const paths = []
    await verifyRepository({ token: 'fixture', request: async (url, options) => {
      expect(options.method).toBe('GET')
      paths.push(new URL(url).pathname)
      return Response.json(new URL(url).pathname.endsWith('/main') ? effective : rulesets.find(set => url.endsWith(`/${set.id}`)))
    } })
    expect(paths).toEqual(['/repos/leapmux/leapmux/rules/branches/main', '/repos/leapmux/leapmux/rulesets/1', '/repos/leapmux/leapmux/rulesets/2'])
    await expect(verifyRepository({ token: 'fixture', request: async () => new Response('', { status: 403 }) })).rejects.toThrow('403')
  })
})
