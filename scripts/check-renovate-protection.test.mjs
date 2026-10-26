import { describe, expect, it } from 'bun:test'
import { checkProtection, verifyRepository } from './check-renovate-protection.mjs'

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
    required_status_checks: ['Required CI', 'NOTICE'].map(context => ({ context, integration_id: 15368 })),
  } })
  return { effective, rulesets }
}

describe('checkProtection', () => {
  it('accepts separate effective rulesets with only an administrator bypass', () => {
    const { effective, rulesets } = fixture()
    expect(() => checkProtection(effective, rulesets)).not.toThrow()
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
      expect(options.method).toBeUndefined()
      paths.push(new URL(url).pathname)
      return Response.json(new URL(url).pathname.endsWith('/main') ? effective : rulesets.find(set => url.endsWith(`/${set.id}`)))
    } })
    expect(paths).toEqual(['/repos/leapmux/leapmux/rules/branches/main', '/repos/leapmux/leapmux/rulesets/1', '/repos/leapmux/leapmux/rulesets/2'])
    await expect(verifyRepository({ token: 'fixture', request: async () => new Response('', { status: 403 }) })).rejects.toThrow('403')
  })
})
