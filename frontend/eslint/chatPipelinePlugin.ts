import type { TSESLint, TSESTree } from '@typescript-eslint/utils'
import type ts from 'typescript'
import { dirname, extname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { AST_NODE_TYPES, ESLintUtils } from '@typescript-eslint/utils'

const createRule = ESLintUtils.RuleCreator(name => `https://leapmux.dev/eslint/${name}`)
const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

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

const noProviderDecision = createRule<[], 'providerDecision'>({
  name: 'no-provider-decision',
  meta: {
    type: 'problem',
    docs: { description: 'Route provider decisions through provider plugins.' },
    schema: [],
    messages: { providerDecision: 'Shared code must not decide by AgentProvider. Move this decision into a provider module.' },
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
    const providerWireToken = /^(?:(?:cursor|_goose|mcp)\/[a-z_/]+|_reasonix\.io\/[a-z_/]+|session\/(?:update|request_permission|new|prompt|load)|interaction\/requestUserInput|tool_call_update|agent_message_chunk|agent_thought_chunk|available_commands_update|session_info_update|config_option_update|commandExecution|fileChange|mcpToolCall|dynamicToolCall|collabAgentToolCall|entry_appended|tool_execution_start|tool_execution_end|agent_settled|compaction_start|compaction_end|tool_use_result|compact_boundary)$/
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
        if (node.callee.type !== AST_NODE_TYPES.MemberExpression || node.callee.computed)
          return
        const method = node.callee.property.type === AST_NODE_TYPES.Identifier ? node.callee.property.name : ''
        if ((method === 'has' || method === 'includes')
          && (isProvider(node.callee.object) || node.arguments.some(argument => argument.type !== AST_NODE_TYPES.SpreadElement && isProvider(argument)))) {
          report(node)
        }
      },
      Property(node) {
        if (node.computed && isProvider(node.key))
          report(node)
      },
      Literal(node) {
        if (typeof node.value === 'string' && providerWireToken.test(node.value))
          report(node)
      },
      TemplateElement(node) {
        if (providerWireToken.test(node.value.cooked))
          report(node)
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

const HOOKS = new Set(['resolveMessage', 'spanRole', 'relatedMessages', 'classify', 'extractRow', 'extractDivider', 'notificationEntry', 'lifecycle'])

const pluginRegistrationOnly = createRule<[], 'inlineHook'>({
  name: 'plugin-registration-only',
  meta: {
    type: 'problem',
    docs: { description: 'Keep provider plugin modules registration-only.' },
    schema: [],
    messages: { inlineHook: 'Import the {{hook}} hook from a job-specific provider module.' },
  },
  defaultOptions: [],
  create(context) {
    if (!/\/components\/chat\/providers\/[^/]+\/plugin\.ts$/.test(context.filename.replaceAll('\\', '/')))
      return {}
    const importedBindings = new Set<string>()
    for (const statement of context.sourceCode.ast.body) {
      if (statement.type !== AST_NODE_TYPES.ImportDeclaration)
        continue
      for (const specifier of statement.specifiers)
        importedBindings.add(specifier.local.name)
    }
    const arrivesFromImport = (node: TSESTree.Node): boolean => {
      if (node.type === AST_NODE_TYPES.Identifier)
        return importedBindings.has(node.name)
      if (node.type !== AST_NODE_TYPES.MemberExpression)
        return false
      let object: TSESTree.Expression = node.object
      while (object.type === AST_NODE_TYPES.MemberExpression)
        object = object.object
      return object.type === AST_NODE_TYPES.Identifier && importedBindings.has(object.name)
    }
    const checkHook = (node: TSESTree.Node, key: string, value: TSESTree.Node): void => {
      if (HOOKS.has(key) && !arrivesFromImport(value))
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

const plugin: TSESLint.FlatConfig.Plugin = {
  rules: {
    'layer-imports': layerImports,
    'no-provider-decision': noProviderDecision,
    'no-forbidden-assertion': noForbiddenAssertion,
    'plugin-registration-only': pluginRegistrationOnly,
  },
}

export default plugin
