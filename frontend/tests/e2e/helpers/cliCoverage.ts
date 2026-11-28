/**
 * Mechanical audit proving every runnable leaf CLI command exposed by
 * `leapmux commands --json` is exercised by at least one Playwright E2E spec
 * under `frontend/tests/e2e/`.
 */

import { execFileSync } from 'node:child_process'
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import process from 'node:process'
import ts from 'typescript'

/** One command node emitted by `leapmux commands --json`. */
export interface CLICommandNode {
  path: string
  group?: boolean
}

/** Command runner function names whose argument-array literal specifies a CLI subcommand. */
const RUNNER_CALLS: Record<string, number> = {
  runCLI: 1,
  runCLIText: 1,
  runCLILocal: 0,
  runCLILocalText: 0,
  spawnCLILive: 1,
  spawnTestProcess: 1,
  spawn: 1,
}

/** Named helper functions that wrap a fixed CLI leaf command. */
const WRAPPER_COMMANDS: Record<string, string> = {
  startControlEventStream: 'events',
  cliAgentOpen: 'agent open',
  setHubSetting: 'admin settings set',
  mintInteractiveOAuthCLI: 'auth login',
}

/**
 * Load all runnable leaf command paths from `leapmux commands --json`.
 * The frontend test targets build the binary before the audit runs.
 */
export function loadRunnableLeafCommands(repoRoot: string): string[] {
  const binName = process.platform === 'win32'
    ? 'leapmux.exe'
    : 'leapmux'
  const raw = execFileSync(join(repoRoot, binName), ['commands', '--json'], {
    encoding: 'utf8',
    maxBuffer: 10 * 1024 * 1024,
  })

  const parsed = JSON.parse(raw) as { data?: { commands?: CLICommandNode[] }, commands?: CLICommandNode[] }
  const nodes = parsed.data?.commands ?? parsed.commands ?? []
  const allPaths = new Set(nodes.map(n => n.path))
  return nodes
    .filter(n => !n.group && !hasChildPath(n.path, allPaths))
    .map(n => n.path)
    .sort()
}

function hasChildPath(path: string, allPaths: ReadonlySet<string>): boolean {
  const prefix = `${path} `
  for (const candidate of allPaths) {
    if (candidate.startsWith(prefix))
      return true
  }
  return false
}

/** Recursively collect every `.spec.ts` file under `e2eDir`. */
export function listE2ESpecFiles(e2eDir: string): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name)
      if (entry.isDirectory())
        walk(full)
      else if (entry.isFile() && entry.name.endsWith('.spec.ts'))
        out.push(full)
    }
  }
  walk(e2eDir)
  return out.sort()
}

/** Unwrap `as const`, parentheses, and non-null assertions to reach the inner expression. */
function unwrapExpression(expr: ts.Expression): ts.Expression {
  let cur = expr
  while (true) {
    if (ts.isAsExpression(cur) || ts.isSatisfiesExpression(cur) || ts.isParenthesizedExpression(cur) || ts.isNonNullExpression(cur))
      cur = cur.expression
    else
      return cur
  }
}

/** Extract static string literal values from an expression (literal, array element, or bound symbol). */
function resolveStringValues(expr: ts.Expression, bindings: ReadonlyMap<string, readonly string[]>): string[] {
  const inner = unwrapExpression(expr)
  if (ts.isStringLiteral(inner) || ts.isNoSubstitutionTemplateLiteral(inner))
    return [inner.text]
  if (ts.isIdentifier(inner)) {
    const bound = bindings.get(inner.text)
    return bound
      ? [...bound]
      : []
  }
  return []
}

/** Expand an array literal of CLI args into all possible concrete leading token sequences. */
function expandArgTokenSequences(
  arrayExpr: ts.ArrayLiteralExpression,
  bindings: ReadonlyMap<string, readonly string[]>,
): string[][] {
  let prefixes: string[][] = [[]]
  for (const elem of arrayExpr.elements) {
    if (ts.isSpreadElement(elem))
      break
    const values = resolveStringValues(elem, bindings)
    if (values.length === 0)
      break
    const next: string[][] = []
    for (const prefix of prefixes) {
      for (const value of values) {
        // Stop collecting command path tokens once we hit a flag (`--foo`).
        if (value.startsWith('-'))
          next.push(prefix)
        else
          next.push([...prefix, value])
      }
    }
    prefixes = next
    // Leaf command paths are at most 5 tokens deep (`daemon worker cross-worker-pins list`).
    if (prefixes[0] && prefixes[0].length >= 5)
      break
  }
  return prefixes
}

/** Collect literal string values passed to each parameter of local functions in a source file. */
function collectLocalFunctionParamValues(sf: ts.SourceFile): Map<string, Map<string, Set<string>>> {
  const paramNamesByFn = new Map<string, string[]>()
  const valuesByFn = new Map<string, Map<string, Set<string>>>()

  const recordFn = (name: string, params: ts.NodeArray<ts.ParameterDeclaration>) => {
    const names = params.map(p => (ts.isIdentifier(p.name)
      ? p.name.text
      : ''))
    paramNamesByFn.set(name, names)
    valuesByFn.set(name, new Map())
  }

  const visitDecls = (node: ts.Node) => {
    if (ts.isFunctionDeclaration(node) && node.name) {
      recordFn(node.name.text, node.parameters)
    }
    else if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.initializer) {
      const init = unwrapExpression(node.initializer)
      if (ts.isArrowFunction(init) || ts.isFunctionExpression(init))
        recordFn(node.name.text, init.parameters)
    }
    ts.forEachChild(node, visitDecls)
  }
  visitDecls(sf)

  const visitCalls = (node: ts.Node) => {
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression)) {
      const fnName = node.expression.text
      const paramNames = paramNamesByFn.get(fnName)
      const paramMap = valuesByFn.get(fnName)
      if (paramNames && paramMap) {
        node.arguments.forEach((arg, idx) => {
          const pName = paramNames[idx]
          if (!pName)
            return
          const inner = unwrapExpression(arg)
          if (ts.isStringLiteral(inner) || ts.isNoSubstitutionTemplateLiteral(inner)) {
            let set = paramMap.get(pName)
            if (!set) {
              set = new Set()
              paramMap.set(pName, set)
            }
            set.add(inner.text)
          }
        })
      }
    }
    ts.forEachChild(node, visitCalls)
  }
  visitCalls(sf)

  return valuesByFn
}

/**
 * Extract all runnable leaf command paths invoked inside one TypeScript E2E spec file.
 */
export function extractCoveredCommandsFromSource(
  sourceText: string,
  fileName: string,
  knownLeafCommands: ReadonlySet<string>,
): Set<string> {
  const sf = ts.createSourceFile(fileName, sourceText, ts.ScriptTarget.Latest, true)
  const fnParamValues = collectLocalFunctionParamValues(sf)
  const covered = new Set<string>()

  const matchTokens = (tokens: readonly string[]) => {
    for (let len = tokens.length; len >= 1; len--) {
      const candidate = tokens.slice(0, len).join(' ')
      if (knownLeafCommands.has(candidate)) {
        covered.add(candidate)
        return
      }
    }
  }

  const visit = (node: ts.Node, bindings: Map<string, readonly string[]>) => {
    // Enter a local function with all literal string values observed at its call sites.
    if ((ts.isFunctionDeclaration(node) && node.name) || (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.initializer && (ts.isArrowFunction(unwrapExpression(node.initializer)) || ts.isFunctionExpression(unwrapExpression(node.initializer))))) {
      const fnName = ts.isFunctionDeclaration(node)
        ? node.name!.text
        : (node.name as ts.Identifier).text
      const paramMap = fnParamValues.get(fnName)
      if (paramMap && paramMap.size > 0) {
        const nextBindings = new Map(bindings)
        for (const [param, vals] of paramMap)
          nextBindings.set(param, [...vals])
        ts.forEachChild(node, child => visit(child, nextBindings))
        return
      }
    }

    // Track `for (const v of ['a', 'b'] as const)` literal-array loop bindings.
    if (ts.isForOfStatement(node)) {
      const iterExpr = unwrapExpression(node.expression)
      if (ts.isArrayLiteralExpression(iterExpr) && ts.isVariableDeclarationList(node.initializer)) {
        const decl = node.initializer.declarations[0]
        if (decl && ts.isIdentifier(decl.name)) {
          const vals: string[] = []
          for (const elem of iterExpr.elements) {
            const inner = unwrapExpression(elem)
            if (ts.isStringLiteral(inner) || ts.isNoSubstitutionTemplateLiteral(inner))
              vals.push(inner.text)
          }
          if (vals.length > 0) {
            const nextBindings = new Map(bindings)
            nextBindings.set(decl.name.text, vals)
            visit(node.statement, nextBindings)
            return
          }
        }
      }
    }

    // Track local `const args = ['a', 'b', ...]` arrays so `runCLI(cli, args)` is resolved.
    if (ts.isBlock(node) || ts.isSourceFile(node)) {
      const localArrays = new Map<string, ts.ArrayLiteralExpression>()
      for (const stmt of node.statements) {
        if (ts.isVariableStatement(stmt)) {
          for (const decl of stmt.declarationList.declarations) {
            if (ts.isIdentifier(decl.name) && decl.initializer) {
              const init = unwrapExpression(decl.initializer)
              if (ts.isArrayLiteralExpression(init))
                localArrays.set(decl.name.text, init)
            }
          }
        }
      }
      if (localArrays.size > 0) {
        const scanStmt = (n: ts.Node) => {
          if (ts.isCallExpression(n) && ts.isIdentifier(n.expression)) {
            const argIdx = RUNNER_CALLS[n.expression.text]
            if (argIdx !== undefined && n.arguments[argIdx]) {
              const arg = unwrapExpression(n.arguments[argIdx])
              if (ts.isIdentifier(arg)) {
                const arr = localArrays.get(arg.text)
                if (arr) {
                  for (const seq of expandArgTokenSequences(arr, bindings))
                    matchTokens(seq)
                }
              }
            }
          }
          ts.forEachChild(n, scanStmt)
        }
        scanStmt(node)
      }
    }

    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression)) {
      const callee = node.expression.text
      const wrapped = WRAPPER_COMMANDS[callee]
      if (wrapped && knownLeafCommands.has(wrapped))
        covered.add(wrapped)

      const argIdx = RUNNER_CALLS[callee]
      if (argIdx !== undefined && node.arguments[argIdx]) {
        const arg = unwrapExpression(node.arguments[argIdx])
        if (ts.isArrayLiteralExpression(arg)) {
          for (const seq of expandArgTokenSequences(arg, bindings))
            matchTokens(seq)
        }
      }
    }

    ts.forEachChild(node, child => visit(child, bindings))
  }

  visit(sf, new Map())
  return covered
}

/** Audit every E2E spec under `e2eDir` against all runnable leaf commands. */
export function auditE2ECLICoverage(repoRoot: string, e2eDir: string): {
  leafCommands: string[]
  covered: Map<string, string[]>
  uncovered: string[]
} {
  const leafCommands = loadRunnableLeafCommands(repoRoot)
  const knownSet = new Set(leafCommands)
  const covered = new Map<string, string[]>()

  for (const specFile of listE2ESpecFiles(e2eDir)) {
    const source = readFileSync(specFile, 'utf8')
    const found = extractCoveredCommandsFromSource(source, specFile, knownSet)
    const rel = relative(e2eDir, specFile)
    for (const cmd of found) {
      const list = covered.get(cmd) ?? []
      list.push(rel)
      covered.set(cmd, list)
    }
  }

  const uncovered = leafCommands.filter(cmd => !covered.has(cmd))
  return { leafCommands, covered, uncovered }
}
