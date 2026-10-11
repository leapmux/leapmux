import type * as TypeScript from 'typescript'
import { dirname, resolve } from 'node:path'
import {
  createSourceFile,
  isExportDeclaration,
  isFunctionDeclaration,
  isImportDeclaration,
  isNamedExports,
  isNamedImports,
  isStringLiteral,
  isVariableStatement,
  ScriptTarget,
} from 'typescript'
import { lineOf } from '~/test-support/syntaxSite'

// The analysis behind the E2E guard that keeps one import path for the plain `expect` of Playwright.
//
// AGENTS.md prefers a direct import to a re-export. A fixture module that re-exported the plain `expect` gave a second
// import path for one value, and the tree then imported it both ways. A module that EXTENDS `expect`
// (`export const expect = base.extend(...)`) owns a value of its own, so an import of that `expect` stays correct.
//
// The analysis reads the syntax tree and finds two shapes:
//
// - A re-export of the plain `expect`: `export { expect } from '@playwright/test'`, or `export { expect }` of an
//   `expect` that the file imports from `@playwright/test`.
// - An import of `expect` from a relative module that declares no `expect` of its own, so that module only passes the
//   value on.
//
// Separate from the guard, as `fixedWaits.ts` is: a guard over the real tree asserts an EMPTY list, and an analysis
// that finds nothing passes it forever. This function takes sources and returns findings, so a case states what it
// must find.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its own. Its cases live in
// `expectImports.test.ts` beside it.

/** The package that owns the plain `expect` of the E2E specs. */
export const PLAYWRIGHT_TEST = '@playwright/test'

/** One source file of the analysis. */
export interface ExpectSourceFile {
  /** The absolute path. */
  path: string
  source: string
}

/** One re-export of the plain `expect`, or one import of it through a module that only passes it on. */
export interface PassedOnExpect {
  path: string
  /** The 1-based line of the statement. */
  line: number
  kind: 'reexport' | 'import'
  /** For an import, the module specifier as the file spells it. */
  module?: string
}

/** The name that an import or export specifier reads from its module. */
function importedName(element: TypeScript.ImportSpecifier | TypeScript.ExportSpecifier): string {
  return (element.propertyName ?? element.name).text
}

/** Whether `file` imports the binding `expect` from Playwright. */
function importsPlaywrightExpect(file: TypeScript.SourceFile): boolean {
  return file.statements.some(statement => isImportDeclaration(statement)
    && isStringLiteral(statement.moduleSpecifier)
    && statement.moduleSpecifier.text === PLAYWRIGHT_TEST
    && !!statement.importClause?.namedBindings
    && isNamedImports(statement.importClause.namedBindings)
    && statement.importClause.namedBindings.elements.some(element => element.name.text === 'expect' && importedName(element) === 'expect'))
}

/** Whether `file` declares a top-level `expect` of its own: a variable or a function. */
function declaresExpect(file: TypeScript.SourceFile): boolean {
  return file.statements.some(statement =>
    (isFunctionDeclaration(statement) && statement.name?.text === 'expect')
    || (isVariableStatement(statement) && statement.declarationList.declarations.some(declaration => declaration.name.getText(file) === 'expect')))
}

/**
 * The text that each finding needs: an export list or a relative import list that names `expect`. A file without it
 * holds no finding, so the analysis parses only the files that match, and the modules that they import. The scan of
 * the whole E2E tree then parses a few files, not two thousand.
 */
const CANDIDATE = /\bexport\s*\{[^}]*\bexpect\b|\bimport\s*\{[^}]*\bexpect\b[^}]*\}\s*from\s*['"]\./

/** Find each re-export of the plain `expect`, and each import of `expect` through a module that only passes it on. */
export function passedOnExpects(inputs: readonly ExpectSourceFile[]): PassedOnExpect[] {
  const sources = new Map(inputs.map(input => [input.path, input.source]))
  const parsed = new Map<string, TypeScript.SourceFile>()
  const parse = (path: string): TypeScript.SourceFile | undefined => {
    const source = sources.get(path)
    if (source === undefined)
      return undefined
    let file = parsed.get(path)
    if (!file) {
      file = createSourceFile(path, source, ScriptTarget.Latest, /* setParentNodes */ true)
      parsed.set(path, file)
    }
    return file
  }
  const findings: PassedOnExpect[] = []
  for (const input of inputs) {
    if (!CANDIDATE.test(input.source))
      continue
    const path = input.path
    const file = parse(path)!
    const plainImported = importsPlaywrightExpect(file)
    for (const statement of file.statements) {
      if (isExportDeclaration(statement) && statement.exportClause && isNamedExports(statement.exportClause)) {
        const fromPlaywright = !!statement.moduleSpecifier && isStringLiteral(statement.moduleSpecifier) && statement.moduleSpecifier.text === PLAYWRIGHT_TEST
        const reexports = statement.exportClause.elements.some(element => importedName(element) === 'expect')
        if (reexports && (fromPlaywright || (!statement.moduleSpecifier && plainImported)))
          findings.push({ path, line: lineOf(file, statement), kind: 'reexport' })
        continue
      }
      if (!isImportDeclaration(statement) || !isStringLiteral(statement.moduleSpecifier) || !statement.moduleSpecifier.text.startsWith('.'))
        continue
      const named = statement.importClause?.namedBindings
      if (!named || !isNamedImports(named) || !named.elements.some(element => importedName(element) === 'expect'))
        continue
      const base = resolve(dirname(path), statement.moduleSpecifier.text)
      const target = parse(`${base}.ts`) ?? parse(resolve(base, 'index.ts'))
      // A module outside the inputs cannot be judged, so it is not a finding.
      if (target && !declaresExpect(target))
        findings.push({ path, line: lineOf(file, statement), kind: 'import', module: statement.moduleSpecifier.text })
    }
  }
  return findings
}
