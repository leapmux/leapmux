import type * as TypeScript from 'typescript'
import { createSourceFile, isCallExpression, isExportDeclaration, isImportDeclaration, isNamedImports, isNamespaceImport, isStringLiteral, ScriptTarget, SyntaxKind } from 'typescript'

// One syntax-tree reader for every import edge a layering guard refuses.
//
// The guards used to scan source text with regular expressions, and each learned
// the forms it knew on the day it was written: one read single quotes alone, one
// miscounted a wrapped statement's opening line, and none saw `import()` or
// `require()` at all -- so a dynamic import could carry a runtime edge past a rule
// written to refuse exactly that edge. The compiler's own tree states every form
// the grammar holds, and comments never reach it.

/** How one edge reaches its module. */
export type ImportEdgeKind
  /** `import ... from '...'` / `import '...'` with no clause. */
  = | 'static'
    /** `export ... from '...'`, which re-exports another module's bindings. */
    | 're-export'
    /** `import('...')`, which loads the module when it runs. */
    | 'dynamic'
    /** `require('...')`, the CommonJS form. */
    | 'require'

/** One import edge a module states, in the terms a layering guard asks about. */
export interface ImportEdge {
  /** The module specifier exactly as written, quotes excluded. */
  specifier: string
  kind: ImportEdgeKind
  /**
   * Whether the edge is TYPE-ONLY: erased by the compiler, so it pulls nothing
   * into the bundle and closes no cycle. Always false for the side-effect and
   * dynamic forms, which exist to run.
   */
  typeOnly: boolean
  /** The 1-based line the statement begins on. */
  line: number
  /** Whether the specifier was written in double quotes. */
  doubleQuoted: boolean
}

/**
 * Every import edge one module states.
 *
 * Covers `import`, `import type`, `export ... from`, side-effect imports,
 * `import('...')` and `require('...')`, in either quote style. A specifier held
 * in a template literal or built from parts is not an edge the tree can name, and
 * this reports nothing for it -- the same answer the bundler would need a runtime
 * to give.
 */
export function moduleImportEdges(source: string, fileName = 'module.ts'): ImportEdge[] {
  const file = createSourceFile(fileName, source, ScriptTarget.Latest, /* setParentNodes */ true)
  const edges: ImportEdge[] = []

  const specifierOf = (literal: TypeScript.StringLiteral): { specifier: string, doubleQuoted: boolean } => ({
    specifier: literal.text,
    doubleQuoted: literal.getText(file).startsWith('"'),
  })

  const visit = (node: TypeScript.Node): void => {
    if (isImportDeclaration(node) && isStringLiteral(node.moduleSpecifier)) {
      const { specifier, doubleQuoted } = specifierOf(node.moduleSpecifier)
      const clause = node.importClause
      edges.push({
        specifier,
        kind: 'static',
        // `import type` states the whole clause; `import { type X }` states one
        // binding and leaves a runtime statement -- only the first erases the edge.
        typeOnly: clause?.isTypeOnly === true,
        line: file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1,
        doubleQuoted,
      })
    }
    else if (isExportDeclaration(node) && node.moduleSpecifier !== undefined && isStringLiteral(node.moduleSpecifier)) {
      const { specifier, doubleQuoted } = specifierOf(node.moduleSpecifier)
      edges.push({
        specifier,
        kind: 're-export',
        typeOnly: node.isTypeOnly === true,
        line: file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1,
        doubleQuoted,
      })
    }
    else if (isCallExpression(node)) {
      // `require('...')` in either arity form; `import('...')` takes exactly one.
      const isRequire = node.expression.kind === SyntaxKind.Identifier && (node.expression as TypeScript.Identifier).text === 'require'
      const isDynamicImport = node.expression.kind === SyntaxKind.ImportKeyword
      if ((isRequire || isDynamicImport) && node.arguments.length >= 1) {
        const argument = node.arguments[0]
        // The arity bound above keeps the read in range; the undefined check is the type-level guard alone.
        if (argument !== undefined && isStringLiteral(argument)) {
          const { specifier, doubleQuoted } = specifierOf(argument)
          edges.push({
            specifier,
            kind: isDynamicImport ? 'dynamic' : 'require',
            typeOnly: false,
            line: file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1,
            doubleQuoted,
          })
        }
      }
    }
    node.forEachChild(visit)
  }
  file.forEachChild(visit)
  return edges
}

/**
 * Every edge whose specifier crosses into a layer directory.
 *
 * The matcher is shared by guards that read opposite directions of one rule, so
 * the two cannot drift apart again -- one guard learning a second quotation style
 * while the other kept the first is what opened the hole this module closes.
 */
export function importEdgesIntoLayer(edges: readonly ImportEdge[], layer: string): ImportEdge[] {
  const fragment = `/${layer}/`
  return edges.filter(edge => edge.specifier.includes(fragment) || edge.specifier.endsWith(`/${layer}`))
}

/** One local name a module's imports introduce, and the module it arrives from. */
export interface ImportedName {
  /** The local name the binding declares: `import { X as Y }` names `Y`. */
  name: string
  /** The module specifier exactly as written, quotes excluded. */
  specifier: string
}

/**
 * Every local name a module's STATIC imports introduce.
 *
 * The default binding (`import X from '...'`), each named binding (`import { Y }`,
 * aliased or not, `import { type Y }` included) and the namespace
 * (`import * as N`) -- the names a later expression can resolve back to the module
 * they arrived from. Re-exports bind nothing in THIS module, and neither do the
 * dynamic, require and side-effect forms, so none of them name anything.
 */
export function importedNames(source: string, fileName = 'module.ts'): ImportedName[] {
  const file = createSourceFile(fileName, source, ScriptTarget.Latest, /* setParentNodes */ true)
  const names: ImportedName[] = []
  const visit = (node: TypeScript.Node): void => {
    if (isImportDeclaration(node) && isStringLiteral(node.moduleSpecifier)) {
      const specifier = node.moduleSpecifier.text
      const clause = node.importClause
      if (clause?.name)
        names.push({ name: clause.name.text, specifier })
      const bindings = clause?.namedBindings
      if (bindings !== undefined) {
        if (isNamespaceImport(bindings)) {
          names.push({ name: bindings.name.text, specifier })
        }
        else if (isNamedImports(bindings)) {
          for (const element of bindings.elements)
            names.push({ name: element.name.text, specifier })
        }
      }
    }
    node.forEachChild(visit)
  }
  file.forEachChild(visit)
  return names
}
