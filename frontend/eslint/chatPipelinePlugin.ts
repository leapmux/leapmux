import type { TSESLint, TSESTree } from '@typescript-eslint/utils'
import type { ProviderFrameKind } from '../src/generated/contracts/provider-frame-kinds'
import { createHash } from 'node:crypto'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, extname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { AST_NODE_TYPES, ESLintUtils } from '@typescript-eslint/utils'
import ts from 'typescript'
import { PROVIDER_FRAME_KINDS } from '../src/generated/contracts/provider-frame-kinds'
import { PROVIDER_WIRE_TOKENS, wireTokenSources } from './providerWireTokens'

const createRule = ESLintUtils.RuleCreator(name => `https://leapmux.dev/eslint/${name}`)
const eslintRoot = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(eslintRoot, '..')

function moduleText(node: TSESTree.Node | null | undefined): string | null {
  return node?.type === AST_NODE_TYPES.Literal && typeof node.value === 'string' ? node.value : null
}

function canonicalModule(filename: string, specifier: string): string {
  const path = specifier.startsWith('~/')
    ? resolve(frontendRoot, 'src', specifier.slice(2))
    : specifier.startsWith('.') ? resolve(dirname(filename), specifier) : specifier
  const extension = extname(path)
  return extension === '.ts' || extension === '.tsx' || extension === '.js' || extension === '.jsx'
    ? path.slice(0, -extension.length)
    : path
}

function pipelineImportIsForbidden(filename: string, specifier: string): boolean {
  const normalizedFilename = filename.replaceAll('\\', '/')
  if (specifier === 'lucide-solid' || specifier.startsWith('lucide-solid/'))
    return normalizedFilename.includes('/components/chat/model/')
  if (/\.(?:css|css\.ts|tsx)$/.test(specifier))
    return normalizedFilename.includes('/components/chat/model/')
  const canonical = canonicalModule(filename, specifier).replaceAll('\\', '/')
  if (normalizedFilename.includes('/components/chat/providers/'))
    return canonical.includes('/components/chat/results/')
  if (normalizedFilename.includes('/components/chat/results/'))
    return canonical.includes('/components/chat/providers/')
  if (!normalizedFilename.includes('/components/chat/model/'))
    return false
  if (canonical.includes('/stores/'))
    return true
  if (/\/components\/chat\/(?:providers|results|controls)(?:\/|$)/.test(canonical))
    return true
  return canonical.includes('/components/')
    && !canonical.includes('/components/chat/model/')
    && !canonical.includes('/components/chat/diff/')
}

const layerImports = createRule<[], 'forbiddenImport'>({
  name: 'layer-imports',
  meta: {
    type: 'problem',
    docs: { description: 'Keep the chat model independent from upper layers.' },
    schema: [],
    messages: { forbiddenImport: 'This import crosses the chat pipeline layer boundary: {{specifier}}.' },
  },
  defaultOptions: [],
  create(context) {
    const filename = context.filename.replaceAll('\\', '/')
    const pipelineLayer = /\/components\/chat\/(?:model|providers|results)\//.test(filename)
    if (!pipelineLayer)
      return {}
    const check = (node: TSESTree.Node, source: TSESTree.Node | null | undefined) => {
      const specifier = moduleText(source)
      if (specifier !== null && pipelineImportIsForbidden(filename, specifier))
        context.report({ node, messageId: 'forbiddenImport', data: { specifier } })
    }
    return {
      ImportDeclaration: node => check(node, node.source),
      ExportNamedDeclaration: node => check(node, node.source),
      ExportAllDeclaration: node => check(node, node.source),
      ImportExpression(node) {
        if (moduleText(node.source) === null)
          context.report({ node, messageId: 'forbiddenImport', data: { specifier: '<computed>' } })
        else
          check(node, node.source)
      },
      TSImportType: node => check(node, node.source),
      TSImportEqualsDeclaration(node) {
        if (node.moduleReference.type === AST_NODE_TYPES.TSExternalModuleReference)
          check(node, node.moduleReference.expression)
      },
      CallExpression(node) {
        if (node.callee.type !== AST_NODE_TYPES.Identifier || node.callee.name !== 'require')
          return
        const source = node.arguments[0]?.type === AST_NODE_TYPES.SpreadElement ? null : node.arguments[0]
        if (moduleText(source) === null)
          context.report({ node, messageId: 'forbiddenImport', data: { specifier: '<computed>' } })
        else
          check(node, source)
      },
    }
  },
})

function typeContainsName(checker: ts.TypeChecker, type: ts.Type, pattern: RegExp, seen = new Set<ts.Type>()): boolean {
  if (seen.has(type))
    return false
  seen.add(type)
  if (pattern.test(checker.typeToString(type)) || pattern.test(type.aliasSymbol?.getName() ?? '') || pattern.test(type.getSymbol()?.getName() ?? ''))
    return true
  if (type.isUnionOrIntersection() && type.types.some(member => typeContainsName(checker, member, pattern, seen)))
    return true
  const references = type as ts.TypeReference
  return [...(type.aliasTypeArguments ?? []), ...(references.typeArguments ?? [])]
    .some(member => typeContainsName(checker, member, pattern, seen))
}

const noProviderDecision = createRule<[], 'providerDecision' | 'wireToken'>({
  name: 'no-provider-decision',
  meta: {
    type: 'problem',
    docs: { description: 'Route provider decisions through provider plugins.' },
    schema: [],
    messages: {
      providerDecision: 'Shared code must not decide by AgentProvider. Move this decision into a provider module.',
      wireToken: '"{{token}}" identifies a frame of {{sources}}. Shared code must not decide by it. Move this decision into the provider module that owns it.',
    },
  },
  defaultOptions: [],
  create(context) {
    const filename = context.filename.replaceAll('\\', '/')
    if (!filename.includes('/components/chat/') || filename.includes('/components/chat/providers/'))
      return {}
    const services = ESLintUtils.getParserServices(context)
    const checker = services.program.getTypeChecker()
    const isProvider = (node: TSESTree.Node): boolean => {
      const tsNode = services.esTreeNodeToTSNodeMap.get(node)
      return typeContainsName(checker, checker.getTypeAtLocation(tsNode), /(?:^|\.)AgentProvider(?:$|\.)/)
        || /\bAgentProvider\b/.test(context.sourceCode.getText(node))
    }
    const isProviderMember = (node: TSESTree.Node): boolean => {
      if (/\bAgentProvider\s*\./.test(context.sourceCode.getText(node)))
        return true
      const type = checker.getTypeAtLocation(services.esTreeNodeToTSNodeMap.get(node))
      return /^(?:\w+\.)*AgentProvider\.[A-Z0-9_]+$/.test(checker.typeToString(type))
    }
    const report = (node: TSESTree.Node) => context.report({ node, messageId: 'providerDecision' })
    const isWireToken = (text: string): boolean => wireTokenSources(PROVIDER_WIRE_TOKENS, text).length > 0
    const reportWireToken = (node: TSESTree.Node, token: string): void => {
      const sources = wireTokenSources(PROVIDER_WIRE_TOKENS, token)
      if (sources.length > 0)
        context.report({ node, messageId: 'wireToken', data: { token, sources: sources.join(', ') } })
    }
    const tsIsProvider = (node: ts.Node): boolean =>
      typeContainsName(checker, checker.getTypeAtLocation(node), /(?:^|\.)AgentProvider(?:$|\.)/)
      || /\bAgentProvider\b/.test(node.getText())
    const tsIsProviderMember = (node: ts.Node): boolean => {
      if (/\bAgentProvider\s*\./.test(node.getText()))
        return true
      return /^(?:\w+\.)*AgentProvider\.[A-Z0-9_]+$/.test(checker.typeToString(checker.getTypeAtLocation(node)))
    }
    const declarationMakesProviderDecision = (declaration: ts.Declaration): boolean => {
      let decision = false
      const visit = (node: ts.Node): void => {
        if (decision)
          return
        if (ts.isBinaryExpression(node)
          && [ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken].includes(node.operatorToken.kind)
          && (tsIsProvider(node.left) || tsIsProvider(node.right))
          && (tsIsProviderMember(node.left) || tsIsProviderMember(node.right))) {
          decision = true
          return
        }
        if (ts.isSwitchStatement(node) && tsIsProvider(node.expression)) {
          decision = true
          return
        }
        if (ts.isElementAccessExpression(node) && node.argumentExpression !== undefined && tsIsProvider(node.argumentExpression)) {
          decision = true
          return
        }
        if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression)) {
          const method = node.expression.name.text
          if ((method === 'get' || method === 'has' || method === 'includes')
            && (tsIsProvider(node.expression.expression) || node.arguments.some(tsIsProvider))) {
            decision = true
            return
          }
        }
        if ((ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) && isWireToken(node.text)) {
          decision = true
          return
        }
        ts.forEachChild(node, visit)
      }
      visit(declaration)
      return decision
    }
    // A node, not only an expression: the map gives a keyword token for some callees,
    // and a keyword has no symbol.
    const symbolAtExpression = (node: ts.Node): ts.Symbol | undefined => {
      let current: ts.Node = node
      while (ts.isParenthesizedExpression(current) || ts.isAsExpression(current) || ts.isTypeAssertionExpression(current) || ts.isNonNullExpression(current) || ts.isSatisfiesExpression(current))
        current = current.expression
      const symbolNode = ts.isPropertyAccessExpression(current) ? current.name : current
      return checker.getSymbolAtLocation(symbolNode)
    }
    const symbolMakesProviderDecision = (initial: ts.Symbol | undefined, seen = new Set<ts.Symbol>()): boolean => {
      if (initial === undefined)
        return false
      const symbol = (initial.flags & ts.SymbolFlags.Alias) !== 0 ? checker.getAliasedSymbol(initial) : initial
      if (seen.has(symbol))
        return false
      seen.add(symbol)
      if (symbol.declarations?.some(declarationMakesProviderDecision))
        return true
      for (const declaration of symbol.declarations ?? []) {
        if (ts.isVariableDeclaration(declaration) && declaration.initializer !== undefined
          && symbolMakesProviderDecision(symbolAtExpression(declaration.initializer), seen)) {
          return true
        }
      }
      return false
    }
    const helperMakesProviderDecision = (node: TSESTree.CallExpression): boolean => {
      const tsCall = services.esTreeNodeToTSNodeMap.get(node)
      if ((checker.getTypeAtLocation(tsCall).flags & ts.TypeFlags.BooleanLike) === 0)
        return false
      const tsCallee = services.esTreeNodeToTSNodeMap.get(node.callee)
      return symbolMakesProviderDecision(symbolAtExpression(tsCallee))
    }
    return {
      BinaryExpression(node) {
        if (['==', '===', '!=', '!=='].includes(node.operator)
          && (isProvider(node.left) || isProvider(node.right))
          && (isProviderMember(node.left) || isProviderMember(node.right))) {
          report(node)
        }
      },
      SwitchStatement(node) {
        if (isProvider(node.discriminant))
          report(node)
      },
      CallExpression(node) {
        if (helperMakesProviderDecision(node)) {
          report(node)
          return
        }
        if (node.callee.type !== AST_NODE_TYPES.MemberExpression || node.callee.computed)
          return
        const method = node.callee.property.type === AST_NODE_TYPES.Identifier ? node.callee.property.name : ''
        if ((method === 'get' || method === 'has' || method === 'includes')
          && (isProvider(node.callee.object) || node.arguments.some(argument => argument.type !== AST_NODE_TYPES.SpreadElement && isProvider(argument)))) {
          report(node)
        }
      },
      MemberExpression(node) {
        if (node.computed && isProvider(node.property))
          report(node)
      },
      Property(node) {
        if (node.computed && isProvider(node.key))
          report(node)
      },
      Literal(node) {
        if (typeof node.value === 'string')
          reportWireToken(node, node.value)
      },
      TemplateElement(node) {
        // `cooked` is null for a tagged template with an invalid escape.
        if (node.value.cooked !== null)
          reportWireToken(node, node.value.cooked)
      },
    }
  },
})

const FORBIDDEN_ASSERTION = /\b(?:ResolvedMessageContent|ToolCall(?:Variant|Spec(?:Variant)?|ResultBase)?|ToolRequestByKind|ToolResultByKind|ToolResult|ParsedCall|ResolvedCall)\b/

const noForbiddenAssertion = createRule<[], 'forbiddenAssertion'>({
  name: 'no-forbidden-assertion',
  meta: {
    type: 'problem',
    docs: { description: 'Prevent assertions from manufacturing resolved or correlated chat types.' },
    schema: [],
    messages: { forbiddenAssertion: 'This assertion manufactures a resolved-message or correlated tool-call type.' },
  },
  defaultOptions: [],
  create(context) {
    const filename = context.filename.replaceAll('\\', '/')
    if (filename.endsWith('/components/chat/model/createToolCall.ts') || filename.endsWith('/components/chat/providers/registry.ts'))
      return {}
    const services = ESLintUtils.getParserServices(context)
    const checker = services.program.getTypeChecker()
    const check = (node: TSESTree.TSAsExpression | TSESTree.TSTypeAssertion) => {
      const tsTypeNode = services.esTreeNodeToTSNodeMap.get(node.typeAnnotation)
      const type = checker.getTypeFromTypeNode(tsTypeNode as ts.TypeNode)
      if (FORBIDDEN_ASSERTION.test(context.sourceCode.getText(node.typeAnnotation)) || typeContainsName(checker, type, FORBIDDEN_ASSERTION))
        context.report({ node, messageId: 'forbiddenAssertion' })
    }
    return { TSAsExpression: check, TSTypeAssertion: check }
  },
})

const pluginRegistrationOnly = createRule<[], 'inlineHook'>({
  name: 'plugin-registration-only',
  meta: {
    type: 'problem',
    docs: { description: 'Keep provider registration modules registration-only.' },
    schema: [],
    messages: { inlineHook: 'Import the {{hook}} hook from a job-specific provider module.' },
  },
  defaultOptions: [],
  create(context) {
    const filename = context.filename.replaceAll('\\', '/')
    const registrationFile = /\/components\/chat\/providers\/[^/]+\/(?:plugin|register[A-Za-z0-9]*Provider)\.ts$/.test(filename)
    if (!registrationFile)
      return {}
    const services = ESLintUtils.getParserServices(context)
    const checker = services.program.getTypeChecker()
    const importedBindings = new Set<string>()
    const registrationParameters = new Set<string>()
    for (const statement of context.sourceCode.ast.body) {
      if (statement.type === AST_NODE_TYPES.ImportDeclaration) {
        for (const specifier of statement.specifiers)
          importedBindings.add(specifier.local.name)
      }
      const declaration = statement.type === AST_NODE_TYPES.ExportNamedDeclaration ? statement.declaration : statement
      if (declaration?.type === AST_NODE_TYPES.FunctionDeclaration && declaration.id?.name.startsWith('register')) {
        for (const parameter of declaration.params) {
          if (parameter.type === AST_NODE_TYPES.Identifier)
            registrationParameters.add(parameter.name)
        }
      }
    }
    const isCallable = (node: TSESTree.Node): boolean => {
      const tsNode = services.esTreeNodeToTSNodeMap.get(node)
      return checker.getTypeAtLocation(tsNode).getCallSignatures().length > 0
    }
    const arrivesFromApprovedSource = (node: TSESTree.Node): boolean => {
      if (node.type === AST_NODE_TYPES.Identifier)
        return importedBindings.has(node.name) || registrationParameters.has(node.name)
      if (node.type === AST_NODE_TYPES.CallExpression) {
        return arrivesFromApprovedSource(node.callee)
          && node.arguments.every(argument => argument.type !== AST_NODE_TYPES.SpreadElement
            && (!isCallable(argument) || arrivesFromApprovedSource(argument)))
      }
      if (node.type === AST_NODE_TYPES.LogicalExpression)
        return arrivesFromApprovedSource(node.left) && arrivesFromApprovedSource(node.right)
      if (node.type === AST_NODE_TYPES.ConditionalExpression)
        return arrivesFromApprovedSource(node.consequent) && arrivesFromApprovedSource(node.alternate)
      if (node.type === AST_NODE_TYPES.TSAsExpression || node.type === AST_NODE_TYPES.TSTypeAssertion || node.type === AST_NODE_TYPES.TSNonNullExpression)
        return arrivesFromApprovedSource(node.expression)
      if (node.type === AST_NODE_TYPES.ChainExpression)
        return arrivesFromApprovedSource(node.expression)
      if (node.type === AST_NODE_TYPES.MemberExpression) {
        let object: TSESTree.Expression = node.object
        while (object.type === AST_NODE_TYPES.MemberExpression)
          object = object.object
        return object.type === AST_NODE_TYPES.Identifier
          && (importedBindings.has(object.name) || registrationParameters.has(object.name))
      }
      return false
    }
    const checkHook = (node: TSESTree.Node, key: string, value: TSESTree.Node): void => {
      if (isCallable(value) && !arrivesFromApprovedSource(value))
        context.report({ node, messageId: 'inlineHook', data: { hook: key } })
    }
    return {
      Property(node) {
        const key = node.key.type === AST_NODE_TYPES.Identifier
          ? node.key.name
          : node.key.type === AST_NODE_TYPES.Literal && typeof node.key.value === 'string' ? node.key.value : ''
        checkHook(node.value, key, node.value)
      },
      PropertyDefinition(node) {
        const key = node.key.type === AST_NODE_TYPES.Identifier ? node.key.name : ''
        if (node.value !== null)
          checkHook(node.value, key, node.value)
      },
    }
  },
})

/**
 * A digest of the rule modules and of the frame kinds that they read.
 *
 * ESLint keys its cache on the resolved configuration, and it writes a plugin into
 * that key as `meta.name@meta.version`. A plugin with no version adds nothing to the
 * key. Then a rule change, or a new frame kind in a contract, keeps the cached result
 * of each unchanged file, and `eslint --cache` passes a file that now breaks a rule.
 */
export function rulesVersion(modules: ReadonlyArray<readonly [name: string, source: string]>, kinds: readonly ProviderFrameKind[]): string {
  const hash = createHash('sha256')
  for (const [name, source] of modules)
    hash.update(`${name}\0${source}\0`)
  hash.update(JSON.stringify(kinds))
  return hash.digest('hex').slice(0, 16)
}

/** Each rule module in this directory, with its source. A test is not rule code. */
function ruleModules(): Array<[name: string, source: string]> {
  return readdirSync(eslintRoot)
    .filter(file => file.endsWith('.ts') && !file.endsWith('.test.ts'))
    .sort()
    .map(file => [file, readFileSync(join(eslintRoot, file), 'utf8')])
}

const plugin: TSESLint.FlatConfig.Plugin = {
  meta: { name: 'chat-pipeline', version: rulesVersion(ruleModules(), PROVIDER_FRAME_KINDS) },
  rules: {
    'layer-imports': layerImports,
    'no-provider-decision': noProviderDecision,
    'no-forbidden-assertion': noForbiddenAssertion,
    'plugin-registration-only': pluginRegistrationOnly,
  },
}

export default plugin
