import type { McpProbeServer } from './mcpProbeServer'
import type { OpenCodeFamilyProviderOptions } from './openCodeEnvironment'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { writeMcpConfirmationServer, writeMcpFormServer } from './mcpFormServer'
import { openCodeFamilyConfig } from './openCodeEnvironment'

/** The typed form server that proves MiMo's empty-confirmation limit. */
export const MIMO_INPUT_FORM_SERVER_NAME = 'input_probe'

export interface MimoEnvironmentOptions extends OpenCodeFamilyProviderOptions {
  /** The run directory. MiMo keeps its data, configuration, state, and cache under one root inside it. */
  runDirectory: string
  mcpEchoServer: McpProbeServer
}

/**
 * Point MiMo Code at the OpenCode family's mock provider, under MiMo's own variable names, and close every request
 * that no test scripts.
 */
export function createMimoEnvironment(options: MimoEnvironmentOptions): Record<string, string> {
  // MiMo keeps its data, configuration, state and cache under one root. It must be
  // an absolute path, because MiMo refuses to start with a relative one.
  const mimoHome = join(options.runDirectory, 'mimocode-home')
  mkdirSync(mimoHome, { recursive: true })
  const confirmationServer = writeMcpConfirmationServer(mimoHome)
  const inputServer = {
    ...writeMcpFormServer(mimoHome, 'mcp-input-form.mjs'),
    name: MIMO_INPUT_FORM_SERVER_NAME,
  }
  // MiMo reads no OPENCODE_* variable, so it takes the same inline provider
  // under its own names. Every switch below stops a request that no test
  // scripts, or a read of the developer's own configuration.
  return {
    MIMOCODE_HOME: mimoHome,
    MIMOCODE_CONFIG_CONTENT: JSON.stringify(mimoCodeConfig(options, [confirmationServer, options.mcpEchoServer, inputServer])),
    MIMOCODE_DISABLE_PROJECT_CONFIG: 'true',
    // The worker pins this one too. It is here so that the configuration states
    // every tool that the specs script.
    MIMOCODE_ENABLE_QUESTION_TOOL: '1',
    // Analytics is ON unless this is `false`, and it posts to Xiaomi's tracker.
    MIMOCODE_ENABLE_ANALYSIS: 'false',
    // Without this, each start fetches the public model catalog from models.dev.
    MIMOCODE_DISABLE_MODELS_FETCH: 'true',
    MIMOCODE_DISABLE_AUTOUPDATE: 'true',
    // The cron scheduler starts turns of its own, and no test scripts them.
    MIMOCODE_EXPERIMENTAL_CRON: 'false',
    // The checkpoint writer is a hidden subagent that calls the model at 40, 60
    // and 80 percent of the context window.
    MIMOCODE_DISABLE_CHECKPOINT: 'true',
    // No CLAUDE.md, Claude Code command or Claude Code MCP server of the
    // developer's reaches the agent.
    MIMOCODE_DISABLE_CLAUDE_CODE: 'true',
    // MiMo reads OPENAI_API_KEY, ANTHROPIC_API_KEY and the rest into providers
    // of its own. The inline provider is the only one a test may reach.
    MIMOCODE_DISABLE_PROVIDER_ENV: 'true',
    MIMOCODE_DISABLE_BUILTIN_SKILLS: 'true',
    MIMOCODE_DISABLE_COMPOSE_SKILLS: 'true',
    MIMOCODE_DISABLE_AGENTS_SKILLS: 'true',
    MIMOCODE_DISABLE_LSP_DOWNLOAD: 'true',
    // The workflow tool is experimental in MiMo 0.1.15 and off by default. A spec
    // runs a workflow through it (`mimoWorkflowToolCall`).
    MIMOCODE_EXPERIMENTAL_WORKFLOW_TOOL: 'true',
  }
}

/**
 * MiMo Code's configuration: the OpenCode family's configuration, which MiMo reads
 * unchanged, and the switches for every model request that no test scripts.
 *
 * - A second model, so that a spec can switch models and read the switch off
 *   the next request.
 * - The shared mock model variants send `reasoning_effort`, so a spec can read
 *   the effort off the next request too.
 * - The family's `enabled_providers` hides MiMo's own built-in providers, so the
 *   model menu holds the mock alone.
 * - `agent.title.disable` stops the title request that otherwise runs beside the
 *   first turn.
 * - `retry` makes a failed request fail once. A retry would consume the next
 *   scripted step.
 * - `snapshot` and `share` keep MiMo from writing git snapshots and from
 *   offering a public link.
 *
 * The configuration holds no `autoupdate` key. MiMo reads that key only from its
 * global config files, never from this inline value, so MIMOCODE_DISABLE_AUTOUPDATE
 * in the environment carries the switch.
 */
function mimoCodeConfig(provider: MimoEnvironmentOptions, servers: readonly McpProbeServer[]): Record<string, unknown> {
  if (new Set(servers.map(server => server.name)).size !== servers.length)
    throw new Error('The MiMo test environment requires distinct MCP server names.')
  const noRetry = { mode: 'bounded', maxRetries: 0 }
  return {
    ...openCodeFamilyConfig(provider, provider.mcpEchoServer),
    mcp: Object.fromEntries(servers.map(server => [
      server.name,
      {
        type: 'local',
        command: [server.command, ...server.args],
      },
    ])),
    agent: {
      title: { disable: true },
    },
    share: 'disabled',
    snapshot: false,
    retry: {
      request: noRetry,
      stream: noRetry,
      network: noRetry,
      server: noRetry,
      rateLimit: noRetry,
      unknown: noRetry,
    },
  }
}
