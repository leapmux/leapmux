import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { writePrivateJSON } from './privateConfigFile'

export type CodebuddyModelReasoning = { supported: false } | { supported: true, defaultEffort: string }

export interface CodebuddyEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The default model, as the endpoint receives it. */
  modelID: string
  /** A second model, so a spec can switch models. */
  alternateModelID: string
  /** The model that CodeBuddy starts on, as the `custom-local:` prefix selects it. */
  startModel: string
  /** An explicit native profile preference; absence keeps the native default. */
  thinkingEnabled?: boolean
  /** Optional reasoning declarations for the two independent native models. */
  primaryReasoning?: CodebuddyModelReasoning
  alternateReasoning?: CodebuddyModelReasoning
}

/**
 * Configure CodeBuddy's private environment.
 *
 * configDir contains models.json and settings.json.
 * CODEBUDDY_CONFIG_DIR and a private HOME isolate that configuration.
 * The switches disable these unscripted startup requests:
 * - Telemetry and Galileo collection.
 * - Trace collection.
 *
 * DISABLE_AUTOUPDATER also turns off CodeBuddy's updater. The `-p` mode that the
 * worker starts never runs it, but a daemon (CODEBUDDY_SESSION_KIND=daemon) does.
 * Claude Code and Letta Code read the same name.
 *
 * CODEBUDDY_DISABLE_SYSTEM_REMINDER_MD turns off the memory reminder: the system reminder that
 * carries the user, project and local memory to the model. CodeBuddy 2.160.0 loads its project
 * memory from each directory between its working directory and the root of the file system
 * (`MemoryLoader.loadProjectMemories`): the first of CODEBUDDY.md, CODEBUDDY.mdc, AGENTS.md and
 * AGENTS.mdc, and the same names in `.codebuddy/`. A git repository does not stop that walk, so
 * the agent would read the sentinel files of the run root (./ancestorInstructions.ts). The variable
 * is CodeBuddy's one switch for that memory, and no spec relies on a memory file. Two other readers
 * of the memory ignore the variable:
 * - The classifier of the Auto permission mode quotes the memory in a context message of its own. No setting turns that message off, so the mock lets it through when it quotes
 *   only the sentinel files (./codebuddySurface.ts).
 * - The conditional rules take only a file with a `paths` frontmatter, so they send no sentinel file.
 */
export function createCodebuddyEnvironment(options: CodebuddyEnvironmentOptions): Record<string, string> {
  const configDir = join(options.homeDir, '.codebuddy')
  mkdirSync(configDir, { recursive: true })
  writePrivateJSON(join(configDir, 'models.json'), codebuddyModels(options))
  writePrivateJSON(join(configDir, 'settings.json'), {
    model: options.startModel,
    ...(options.thinkingEnabled === undefined
      ? {}
      : { alwaysThinkingEnabled: options.thinkingEnabled }),
  })
  return {
    CODEBUDDY_CONFIG_DIR: configDir,
    DISABLE_TELEMETRY: '1',
    DISABLE_GALILEO: '1',
    DISABLE_AUTOUPDATER: '1',
    CODEBUDDY_DISABLE_TRACE_COLLECTOR: '1',
    CODEBUDDY_DISABLE_SYSTEM_REMINDER_MD: '1',
  }
}

/**
 * Configure CodeBuddy's native custom-local model catalog.
 *
 * CodeBuddy reads models.json from CODEBUDDY_CONFIG_DIR and selects entries through the custom-local: prefix.
 * Each URL must end in /chat/completions.
 * CodeBuddy always sends stream: true and requires Server-Sent Events (SSE).
 * A plain JSON completion causes error_during_execution.
 */
function codebuddyModels(options: CodebuddyEnvironmentOptions): Record<string, unknown> {
  const transport = {
    vendor: 'Mock',
    apiKey: options.modelKey,
    maxInputTokens: 128_000,
    maxOutputTokens: 4096,
    url: `${options.baseURL}/chat/completions`,
    temperature: 0,
    supportsToolCall: true,
    // CodeBuddy drops image and document blocks before the model request
    // when this catalog entry declares text-only input.
    supportsImages: true,
  }
  return {
    models: [
      {
        id: options.modelID,
        name: 'Mock Model',
        ...transport,
        ...codebuddyReasoningProperties(options.primaryReasoning),
      },
      {
        id: options.alternateModelID,
        name: 'Alternate Mock Model',
        ...transport,
        ...codebuddyReasoningProperties(options.alternateReasoning),
      },
    ],
    availableModels: [options.modelID, options.alternateModelID],
  }
}

function codebuddyReasoningProperties(reasoning: CodebuddyModelReasoning | undefined): Record<string, unknown> {
  if (reasoning === undefined)
    return {}
  return reasoning.supported
    ? {
        supportsReasoning: true,
        reasoning: { defaultEffort: reasoning.defaultEffort },
      }
    : { supportsReasoning: false }
}
