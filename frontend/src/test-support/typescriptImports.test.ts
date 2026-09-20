import { describe, expect, it } from 'vitest'
import { importEdgesIntoLayer, importedNames, moduleImportEdges } from './typescriptImports'

// The scanner is the floor every layering guard stands on: a form it misses is a
// form a guard silently stops refusing. Each case here pins one form of the
// grammar, and the comment case pins that documentation can never be an offence.

describe('moduleImportEdges', () => {
  it('reads a static import, in either quote style', () => {
    const single = moduleImportEdges(`import { createToolCall } from './model/createToolCall'`)
    expect(single).toEqual([expect.objectContaining({ specifier: './model/createToolCall', kind: 'static', typeOnly: false, doubleQuoted: false })])

    const double = moduleImportEdges(`import { createToolCall } from "./model/createToolCall"`)
    expect(double).toEqual([expect.objectContaining({ specifier: './model/createToolCall', kind: 'static', doubleQuoted: true })])
  })

  it('reads a type-only import as an erased edge', () => {
    const edges = moduleImportEdges(`
import type { ChatRow } from './model/row'
import { type ToolKind } from './model/toolKind'
`)
    expect(edges).toEqual([
      expect.objectContaining({ specifier: './model/row', typeOnly: true }),
      // One TYPE binding inside a VALUE statement: the statement still runs.
      expect.objectContaining({ specifier: './model/toolKind', typeOnly: false }),
    ])
  })

  it('reads a re-export, including the type-only form', () => {
    const edges = moduleImportEdges(`
export { createToolCall } from './model/createToolCall'
export type { ChatRow } from './model/row'
`)
    expect(edges).toEqual([
      expect.objectContaining({ specifier: './model/createToolCall', kind: 're-export', typeOnly: false }),
      expect.objectContaining({ specifier: './model/row', kind: 're-export', typeOnly: true }),
    ])
  })

  it('reads a side-effect import', () => {
    const edges = moduleImportEdges(`import './providers/claude/plugin'`)
    expect(edges).toEqual([expect.objectContaining({ specifier: './providers/claude/plugin', kind: 'static', typeOnly: false })])
  })

  it('reads a dynamic import, which no textual guard saw', () => {
    const edges = moduleImportEdges(`const plugin = import('./providers/claude/plugin')`)
    expect(edges).toEqual([expect.objectContaining({ specifier: './providers/claude/plugin', kind: 'dynamic', typeOnly: false })])
  })

  it('reads a CommonJS require', () => {
    const edges = moduleImportEdges(`const ts = require('typescript')`)
    expect(edges).toEqual([expect.objectContaining({ specifier: 'typescript', kind: 'require', typeOnly: false })])
  })

  it('reports the line the STATEMENT begins on, not the line the specifier sits on', () => {
    const edges = moduleImportEdges([
      'import {',
      '  createToolCall,',
      '  buildToolCall,',
      '} from \'./model/createToolCall\'',
    ].join('\n'))
    expect(edges).toEqual([expect.objectContaining({ line: 1 })])
  })

  it('reads no import out of a comment, however lifelike the example', () => {
    const edges = moduleImportEdges([
      '// The rule refuses `import { Eye } from "lucide-solid"`, and this line',
      '// explains import("./providers/claude/plugin") with a quoted specifier.',
      '/* import "./providers/claude/plugin" */',
      'const kept = 1',
    ].join('\n'))
    expect(edges).toEqual([])
  })

  it('reports nothing for a specifier the tree cannot name', () => {
    const edges = moduleImportEdges(`const path = cond ? './a' : './b'\nconst mod = import(path)`)
    expect(edges).toEqual([])
  })
})

describe('importEdgesIntoLayer', () => {
  const edges = moduleImportEdges([
    `import type { ChatRow } from '~/components/chat/model/row'`,
    `import { createToolCall } from '../model/createToolCall'`,
    `import { render } from './renderers'`,
  ].join('\n'))

  it('finds the edges whose specifier crosses into the layer', () => {
    expect(importEdgesIntoLayer(edges, 'model').map(edge => edge.specifier)).toEqual(['~/components/chat/model/row', '../model/createToolCall'])
  })

  it('finds an edge that names the layer directory itself', () => {
    const intoProviders = moduleImportEdges(`import { x } from '~/components/chat/providers'`)
    expect(importEdgesIntoLayer(intoProviders, 'providers')).toHaveLength(1)
  })
})

describe('importedNames', () => {
  it('reads the default, named, aliased and namespace bindings of one statement', () => {
    const names = importedNames([
      `import def from './default'`,
      `import { alpha, beta as betaLocal } from './named'`,
      `import * as ns from './namespace'`,
    ].join('\n'))
    expect(names).toEqual([
      { name: 'def', specifier: './default' },
      { name: 'alpha', specifier: './named' },
      { name: 'betaLocal', specifier: './named' },
      { name: 'ns', specifier: './namespace' },
    ])
  })

  it('reads a type-only named binding, which still names a local', () => {
    const names = importedNames(`import { type ChatRow } from './model/row'`)
    expect(names).toEqual([{ name: 'ChatRow', specifier: './model/row' }])
  })

  it('names nothing for the re-export, side-effect, dynamic and require forms', () => {
    const names = importedNames([
      `export { createToolCall } from './model/createToolCall'`,
      `import './providers/claude/plugin'`,
      `const plugin = import('./providers/claude/plugin')`,
      `const ts = require('typescript')`,
    ].join('\n'))
    expect(names).toEqual([])
  })

  it('reads no name out of a comment, however lifelike the example', () => {
    const names = importedNames([
      '// The rule resolves `import { classify } from \'./classification\'`,',
      '// and this line names an import { example } from "./commented" too.',
      'const kept = 1',
    ].join('\n'))
    expect(names).toEqual([])
  })
})
