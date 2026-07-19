import { describe, expect, it } from 'vitest'
import { orgHomePath } from './orgRoutes'

describe('orgHomePath', () => {
  it('builds the /o/{slug} org-home path', () => {
    expect(orgHomePath('alice')).toBe('/o/alice')
  })

  it('passes the slug through verbatim (the caller resolves normalization)', () => {
    expect(orgHomePath('Team-One')).toBe('/o/Team-One')
  })
})
