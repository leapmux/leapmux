import { describe, expect, it } from 'bun:test'
import { registerRootHelper, renovateHandlebars } from './renovate-engine.mjs'

describe('registerRootHelper', () => {
  const handlebars = renovateHandlebars()
  const name = 'leapmuxEngineTestHelper'
  const register = () => registerRootHelper(name, {
    accepts: root => root.allowed === true,
    rejection: 'The test helper renders only at an allowed root.',
  }, root => `rendered ${root.value}`)
  it('renders at the root context of a template that it accepts', () => {
    const dispose = register()
    try {
      expect(handlebars.compile(`{{${name}}}`)({ allowed: true, value: 'x' })).toBe('rendered x')
    }
    finally {
      dispose()
    }
  })
  it('throws the rejection for a root that it does not accept, and inside a nested context', () => {
    const dispose = register()
    try {
      expect(() => handlebars.compile(`{{${name}}}`)({ allowed: false })).toThrow('The test helper renders only at an allowed root.')
      // A nested block gives the helper a context that is not the root, even an accepted one.
      expect(() => handlebars.compile(`{{#with child}}{{${name}}}{{/with}}`)({
        allowed: true,
        child: { allowed: true },
      })).toThrow('The test helper renders only at an allowed root.')
      expect(() => handlebars.compile(`{{#each items}}{{${name}}}{{/each}}`)({
        allowed: true,
        items: [
          { allowed: true },
        ],
      })).toThrow('The test helper renders only at an allowed root.')
    }
    finally {
      dispose()
    }
  })
  it('restores the helper that the name held before, and removes a helper that it added', () => {
    expect(handlebars.helpers[name]).toBeUndefined()
    const dispose = register()
    expect(handlebars.helpers[name]).toBeFunction()
    dispose()
    expect(handlebars.helpers[name]).toBeUndefined()
    const previous = () => 'previous'
    handlebars.registerHelper(name, previous)
    try {
      register()()
      expect(handlebars.helpers[name]).toBe(previous)
    }
    finally {
      handlebars.unregisterHelper(name)
    }
  })
})
