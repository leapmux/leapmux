import type * as TypeScript from 'typescript'
import { createSourceFile, forEachChild, isCallExpression, isIdentifier, ScriptTarget } from 'typescript'
import { describe, expect, it } from 'vitest'
import { enclosingFunctionName, lineOf } from '~/test-support/syntaxSite'

/** Parse `source` and return the file with the first call of `mark()`. */
function markedCall(source: string): { file: TypeScript.SourceFile, call: TypeScript.Node } {
  const file = createSourceFile('/source.ts', source, ScriptTarget.Latest, /* setParentNodes */ true)
  let call: TypeScript.Node | undefined
  const visit = (node: TypeScript.Node): void => {
    if (!call && isCallExpression(node) && isIdentifier(node.expression) && node.expression.text === 'mark')
      call = node
    forEachChild(node, visit)
  }
  visit(file)
  if (!call)
    throw new Error('The source holds no mark() call.')
  return { file, call }
}

describe('lineOf', () => {
  it('returns the 1-based line where the node starts', () => {
    const { file, call } = markedCall('const a = 1\n\n  mark()\n')
    expect(lineOf(file, call)).toBe(3)
  })
})

describe('enclosingFunctionName', () => {
  it('returns the name of a function declaration', () => {
    expect(enclosingFunctionName(markedCall('function hold() { mark() }').call)).toBe('hold')
  })

  it('returns the name of the variable that an arrow function or a function expression initializes', () => {
    expect(enclosingFunctionName(markedCall('const settle = async () => { mark() }').call)).toBe('settle')
    expect(enclosingFunctionName(markedCall('const settle = function () { mark() }').call)).toBe('settle')
  })

  it('passes an anonymous callback and returns the named function outside it', () => {
    expect(enclosingFunctionName(markedCall('function outer() { items.forEach(() => mark()) }').call)).toBe('outer')
  })

  it('returns the innermost name of two nested named functions', () => {
    expect(enclosingFunctionName(markedCall('function outer() { function inner() { mark() } }').call)).toBe('inner')
  })

  it('returns an empty name at the top level and in a callback that no named function holds', () => {
    expect(enclosingFunctionName(markedCall('mark()').call)).toBe('')
    expect(enclosingFunctionName(markedCall('test(\'case\', async () => { mark() })').call)).toBe('')
  })
})
