import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentProvider as Provider } from '~/generated/proto/leapmux/v1/agent_pb'

/**
 * One saved permission decision, in the two halves the worker stores for it.
 *
 * `request` is the provider section of `supplemental_content`, and `response` is the
 * agent's own bytes from `messages.content`. The worker metadata is deliberately absent:
 * it carries a per-request claim token, which is a credential, and the displayed words
 * never derive from it.
 */
export interface SavedDecisionCapture {
  provider: AgentProvider
  /** A name for the case, used as the test title. */
  name: string
  /** What the transcript row reads, measured in a cold browser. */
  label: string
  request: Record<string, unknown>
  response: Record<string, unknown>
}

/**
 * Saved decisions CAPTURED from the installed runtimes on September 14, 2026, rather
 * than written by hand (RL-001).
 *
 * Five providers are here because five are the ones whose runtimes asked for permission.
 * Claude Code, Cursor, OpenCode, Kilo and Pi approved the same operations on their own:
 * the first two have a mode that asks and did not use it for a write inside their own
 * working directory, and the last three expose no permission control at all.
 *
 * Each entry pins one native wire shape to the words the reader sees. A runtime that
 * changes that shape fails the corpus test instead of quietly degrading a reloaded row to
 * the generic "Responded".
 */
export const SAVED_DECISION_CORPUS: readonly SavedDecisionCapture[] = [
  // Codex answers with a bare decision word and states no option list.
  {
    provider: Provider.CODEX,
    name: 'codex Allow',
    label: 'Allow',
    request: {
      method: 'item/fileChange/requestApproval',
      id: 0,
      params: {
        threadId: '01a09eee-c9e5-7af0-821f-8263b3228ae8',
        turnId: '01a09f1a-6b3f-7863-857c-1983b4eaf0ef',
        itemId: 'exec-bdce7ddf-fe2f-4f8b-a807-c2a038d43e5c',
        startedAtMs: 1789375706740,
        reason: null,
        grantRoot: null,
      },
    },
    response: {
      jsonrpc: '2.0',
      id: 0,
      result: {
        decision: 'accept',
      },
    },
  },
  // Copilot names its own approval kind, and `approve-once` is the single-use one.
  {
    provider: Provider.GITHUB_COPILOT,
    name: 'github-copilot Allow once',
    label: 'Allow once',
    request: {
      jsonrpc: '2.0',
      method: 'session.event',
      params: {
        sessionId: '4d29e3a0-ea62-4580-8080-55c32e89058c',
        event: {
          type: 'permission.requested',
          data: {
            requestId: 'da1d347c-00fa-4b9a-ad64-874f9c2bb5a3',
            permissionRequest: {
              kind: 'shell',
              toolCallId: 'call_9s8QPL2WSVY7xwYumO7hAgOX',
              fullCommandText: 'ls -1',
              intention: 'List current directory entries',
              commands: [
                {
                  identifier: 'ls -1',
                  readOnly: false,
                },
              ],
              commandSegments: [
                {
                  identifier: 'ls',
                  fullCommandText: 'ls -1',
                },
              ],
              possiblePaths: [],
              possibleUrls: [],
              hasWriteFileRedirection: false,
              canOfferSessionApproval: false,
            },
            promptRequest: {
              kind: 'commands',
              fullCommandText: 'ls -1',
              intention: 'List current directory entries',
              commandIdentifiers: [
                'ls -1',
              ],
              canOfferSessionApproval: false,
              toolCallId: 'call_9s8QPL2WSVY7xwYumO7hAgOX',
              assistedApproval: {
                recommendation: 'approve',
                reason: 'User explicitly requested listing current directory entries with `ls -1`; it is harmless read-only inspection.',
                model: 'gpt-5.6-luna',
              },
            },
            agentMode: 'interactive',
          },
          id: '6be5cc1f-e115-4f72-b624-ad288cbd1b9f',
          timestamp: '2026-09-14T08:02:41.484Z',
          parentId: '394bc080-5aee-469e-ac1d-c1c7a0ce3b12',
        },
      },
    },
    response: {
      response: {
        request_id: 'copilot-HPfsGc_Bya70ZnVHt8Ac0GUO4VJoSaaMjmUCGqE6RlU',
        response: {
          kind: 'approve-once',
        },
        subtype: 'success',
      },
      type: 'control_response',
    },
  },
  // The location scope carries a second `approval.kind` beside the decision.
  {
    provider: Provider.GITHUB_COPILOT,
    name: 'github-copilot Allow for this project',
    label: 'Allow for this project',
    request: {
      jsonrpc: '2.0',
      method: 'session.event',
      params: {
        sessionId: '4d29e3a0-ea62-4580-8080-55c32e89058c',
        event: {
          type: 'permission.requested',
          data: {
            requestId: '09aef253-4031-438c-b31f-abce2bc8f8d3',
            permissionRequest: {
              kind: 'write',
              toolCallId: 'custom_call_XHRLjJLbxJTEhzZX19TApHPv',
              intention: 'Create file',
              fileName: '/work/probe-two.txt',
              diff: '\ndiff --git a/work/probe-two.txt b/work/probe-two.txt\ncreate file mode 100644\nindex 0000000..0000000\n--- a/dev/null\n+++ b/work/probe-two.txt\n@@ -1,0 +1,2 @@\n+beta\n+\n\n',
              newFileContents: 'beta\n',
              canOfferSessionApproval: true,
            },
            promptRequest: {
              kind: 'write',
              toolCallId: 'custom_call_XHRLjJLbxJTEhzZX19TApHPv',
              intention: 'Create file',
              fileName: '/work/probe-two.txt',
              diff: '\ndiff --git a/work/probe-two.txt b/work/probe-two.txt\ncreate file mode 100644\nindex 0000000..0000000\n--- a/dev/null\n+++ b/work/probe-two.txt\n@@ -1,0 +1,2 @@\n+beta\n+\n\n',
              newFileContents: 'beta\n',
              canOfferSessionApproval: true,
              assistedApproval: {
                recommendation: 'requireApproval',
                reason: 'Proposed file name and content differ from the user\'s authorized request.',
                model: 'gpt-5.6-luna',
              },
            },
            agentMode: 'interactive',
          },
          id: 'ea864f5f-0c4f-463f-8e2c-9e2064ad7d0e',
          timestamp: '2026-09-14T08:12:24.993Z',
          parentId: '7774c31d-8b92-4134-a4ce-3635720b7d92',
        },
      },
    },
    response: {
      response: {
        request_id: 'copilot-ySEH4ZYy1nWBJijF3XCHFJ0jQM2Jm3AI4785BVe0lJg',
        response: {
          approval: {
            kind: 'write',
          },
          kind: 'approve-for-location',
        },
        subtype: 'success',
      },
      type: 'control_response',
    },
  },
  // Goose names every option after its own id, so the words come from the option `kind` (ACP-004).
  {
    provider: Provider.GOOSE,
    name: 'goose Allow once',
    label: 'Allow once',
    request: {
      jsonrpc: '2.0',
      id: '9f6040cd-a7e4-49bf-b497-f73c73b09d3f',
      method: 'session/request_permission',
      params: {
        sessionId: '20260914_21',
        toolCall: {
          toolCallId: 'call_400f32a30c3c49708c3d1995',
          kind: 'other',
          status: 'pending',
          title: 'todo: todo write',
          rawInput: {
            content: '- [ ] Run `ls -1` in the current directory via the shell tool\n- [ ] Report its output to the user',
          },
        },
        options: [
          {
            optionId: 'allow_always',
            name: 'allow_always',
            kind: 'allow_always',
          },
          {
            optionId: 'allow_once',
            name: 'allow_once',
            kind: 'allow_once',
          },
          {
            optionId: 'reject_once',
            name: 'reject_once',
            kind: 'reject_once',
          },
          {
            optionId: 'reject_always',
            name: 'reject_always',
            kind: 'reject_always',
          },
        ],
      },
    },
    response: {
      jsonrpc: '2.0',
      id: '9f6040cd-a7e4-49bf-b497-f73c73b09d3f',
      result: {
        outcome: {
          outcome: 'selected',
          optionId: 'allow_once',
        },
      },
    },
  },
  // Reasonix writes its own option names, and its single-use one is a bare "Allow".
  {
    provider: Provider.REASONIX,
    name: 'reasonix Allow',
    label: 'Allow',
    request: {
      jsonrpc: '2.0',
      id: 1,
      method: 'session/request_permission',
      params: {
        sessionId: '1901ef1a-52bf-4fe8-974a-5b36c29ef6ef',
        toolCall: {
          toolCallId: 'gate-1',
          title: 'write_file reload-probe.txt',
          kind: 'edit',
          status: 'pending',
          rawInput: {
            content: 'alpha',
            path: 'reload-probe.txt',
          },
          locations: [
            {
              path: '/work/reload-probe.txt',
            },
          ],
          _meta: {
            'reasonix.io': {
              approvalId: '1',
              fresh: false,
              subject: 'reload-probe.txt',
              tool: 'write_file',
            },
          },
        },
        options: [
          {
            optionId: 'allow_once',
            name: 'Allow',
            kind: 'allow_once',
          },
          {
            optionId: 'allow_always',
            name: 'Allow Edit for this session',
            kind: 'allow_always',
          },
          {
            optionId: 'reject_once',
            name: 'Reject',
            kind: 'reject_once',
          },
        ],
      },
    },
    response: {
      jsonrpc: '2.0',
      id: 1,
      result: {
        outcome: {
          outcome: 'selected',
          optionId: 'allow_once',
        },
      },
    },
  },
  {
    provider: Provider.REASONIX,
    name: 'reasonix Allow Bash=ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt for this session',
    label: 'Allow Bash=ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt for this session',
    request: {
      jsonrpc: '2.0',
      id: 5,
      method: 'session/request_permission',
      params: {
        sessionId: '1901ef1a-52bf-4fe8-974a-5b36c29ef6ef',
        toolCall: {
          toolCallId: 'gate-5',
          title: 'bash ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt',
          kind: 'execute',
          status: 'pending',
          rawInput: {
            command: 'ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt',
          },
          _meta: {
            'reasonix.io': {
              approvalId: '5',
              fresh: false,
              subject: 'ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt',
              tool: 'bash',
            },
          },
        },
        options: [
          {
            optionId: 'allow_once',
            name: 'Allow',
            kind: 'allow_once',
          },
          {
            optionId: 'allow_always',
            name: 'Allow Bash=ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt for this session',
            kind: 'allow_always',
          },
          {
            optionId: 'reject_once',
            name: 'Reject',
            kind: 'reject_once',
          },
        ],
      },
    },
    response: {
      jsonrpc: '2.0',
      id: 5,
      result: {
        outcome: {
          outcome: 'selected',
          optionId: 'allow_always',
        },
      },
    },
  },
  // ZCode answers with a decision word and a reason, and offers no approval scope.
  {
    provider: Provider.ZCODE,
    name: 'zcode Allow',
    label: 'Allow',
    request: {
      type: 'control_request',
      request_id: 'perm_0aa4972e-11ff-468d-9795-014ea21b46e8',
      id: 'server-3',
      method: 'interaction/requestPermission',
      request: {
        tool_name: 'Write',
        tool_use_id: 'call_a34f7e8c209048e88a6768ae',
        input: {
          file_path: '/work/reload-probe.txt',
          content: 'alpha',
        },
      },
      params: {
        input: {
          file_path: '/work/reload-probe.txt',
          content: 'alpha',
        },
        reason: 'Tool has side effects and requires approval',
        requestId: 'perm_0aa4972e-11ff-468d-9795-014ea21b46e8',
        riskLevel: 'medium',
        sessionId: 'sess_7967eb7a-1f1c-4df1-8e2a-0f53459d3c77',
        options: [
          {
            kind: 'allow_once',
            name: 'Allow once',
            optionId: 'allow_once',
            response: {
              decision: 'allow',
              reason: 'Approved once',
            },
          },
          {
            description: 'Do not ask again for matching requests in this project',
            kind: 'allow_always',
            name: 'Always allow in this project',
            optionId: 'allow_project',
            response: {
              decision: 'allow',
              permissionUpdates: [
                {
                  behavior: 'allow',
                  rules: [
                    {
                      toolName: 'Write',
                      ruleContent: '/work/reload-probe.txt',
                    },
                  ],
                  type: 'addRules',
                },
              ],
              reason: 'Approved for this project',
            },
          },
          {
            kind: 'deny',
            name: 'Deny',
            optionId: 'deny',
            response: {
              decision: 'deny',
              reason: 'Denied',
            },
          },
        ],
        toolCallId: 'call_a34f7e8c209048e88a6768ae',
        toolName: 'Write',
        turnId: 'turn_f78e2d5b-99be-415b-82e1-ff91e03f30ad',
      },
    },
    response: {
      id: 'server-3',
      result: {
        decision: 'allow',
        reason: 'Approved once',
      },
    },
  },
]
