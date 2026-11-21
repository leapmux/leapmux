use std::sync::{
    atomic::{AtomicU64, Ordering},
    Arc, Mutex,
};
use std::time::Duration;

use serde::Serialize;
use tauri::Emitter;
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogKind};
use tokio::sync::oneshot;

use crate::{proto, recover, DesktopShell, MAIN_WINDOW_LABEL};

/// Caps one busy-work inspection RPC against the Go sidecar. Sized just above
/// the Worker's 5-second `terminalProcessScanTimeout` so a normal OS process
/// scan finishes cleanly while a wedged sidecar still fails open.
pub(crate) const BUSY_WORK_INSPECT_TIMEOUT: Duration = Duration::from_secs(6);

/// How long the shell waits for the webview to acknowledge `app:confirm-quit`
/// before falling back to a native OS confirmation dialog.
pub(crate) const WEBVIEW_ACK_TIMEOUT: Duration = Duration::from_secs(2);

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct BusyAgentWork {
    pub tab_id: String,
    pub title: String,
    pub active_background_tasks: Vec<String>,
    pub active_child_agents: Vec<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct BusyTerminalProcess {
    pub pid: i32,
    pub name: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct BusyTerminalWork {
    pub tab_id: String,
    pub title: String,
    pub processes: Vec<BusyTerminalProcess>,
    pub total_count: i32,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct SoloBusyWork {
    pub agents: Vec<BusyAgentWork>,
    pub terminals: Vec<BusyTerminalWork>,
}

impl SoloBusyWork {
    pub(crate) fn is_empty(&self) -> bool {
        self.agents.is_empty() && self.terminals.is_empty()
    }

    pub(crate) fn from_proto(resp: proto::InspectSoloBusyWorkResponse) -> Self {
        Self {
            agents: resp
                .agents
                .into_iter()
                .map(|a| BusyAgentWork {
                    tab_id: a.tab_id,
                    title: a.title,
                    active_background_tasks: a.active_background_tasks,
                    active_child_agents: a.active_child_agents,
                })
                .collect(),
            terminals: resp
                .terminals
                .into_iter()
                .map(|t| BusyTerminalWork {
                    tab_id: t.tab_id,
                    title: t.title,
                    processes: t
                        .processes
                        .into_iter()
                        .map(|p| BusyTerminalProcess {
                            pid: p.pid,
                            name: p.name,
                        })
                        .collect(),
                    total_count: t.total_count,
                })
                .collect(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct QuitConfirmPayload {
    pub prompt_id: u64,
    pub busy_work: SoloBusyWork,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum InspectStart {
    Started,
    AlreadyInspecting,
    AlreadyPrompting,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PromptResolution {
    Confirmed,
    Cancelled,
    Ignored,
}

enum PromptOwner {
    AwaitingAck(oneshot::Sender<()>),
    Webview,
    NativeFallback,
}

enum QuitGuardState {
    Idle,
    Inspecting,
    Prompting { prompt_id: u64, owner: PromptOwner },
}

/// Coordinates solo-mode busy-work inspection and single-flight quit
/// confirmation across the webview dialog and the native fallback dialog.
pub(crate) struct QuitGuard {
    next_prompt_id: AtomicU64,
    state: Mutex<QuitGuardState>,
}

impl Default for QuitGuard {
    fn default() -> Self {
        Self {
            next_prompt_id: AtomicU64::new(1),
            state: Mutex::new(QuitGuardState::Idle),
        }
    }
}

impl QuitGuard {
    pub(crate) fn begin_inspect(&self) -> InspectStart {
        let mut guard = recover(&self.state);
        match *guard {
            QuitGuardState::Idle => {
                *guard = QuitGuardState::Inspecting;
                InspectStart::Started
            }
            QuitGuardState::Inspecting => InspectStart::AlreadyInspecting,
            QuitGuardState::Prompting { .. } => InspectStart::AlreadyPrompting,
        }
    }

    pub(crate) fn finish_inspect_idle(&self) {
        let mut guard = recover(&self.state);
        if matches!(*guard, QuitGuardState::Inspecting) {
            *guard = QuitGuardState::Idle;
        }
    }

    pub(crate) fn transition_to_prompt(&self) -> Option<(u64, oneshot::Receiver<()>)> {
        let mut guard = recover(&self.state);
        if !matches!(*guard, QuitGuardState::Inspecting) {
            return None;
        }
        let prompt_id = self.next_prompt_id.fetch_add(1, Ordering::Relaxed);
        let (ack_tx, ack_rx) = oneshot::channel();
        *guard = QuitGuardState::Prompting {
            prompt_id,
            owner: PromptOwner::AwaitingAck(ack_tx),
        };
        Some((prompt_id, ack_rx))
    }

    pub(crate) fn acknowledge_webview(&self, prompt_id: u64) -> bool {
        let mut guard = recover(&self.state);
        let matches_awaiting = matches!(
            *guard,
            QuitGuardState::Prompting {
                prompt_id: current,
                owner: PromptOwner::AwaitingAck(_),
            } if current == prompt_id
        );
        if !matches_awaiting {
            return false;
        }
        let previous = std::mem::replace(
            &mut *guard,
            QuitGuardState::Prompting {
                prompt_id,
                owner: PromptOwner::Webview,
            },
        );
        drop(guard);
        if let QuitGuardState::Prompting {
            owner: PromptOwner::AwaitingAck(ack_tx),
            ..
        } = previous
        {
            let _ = ack_tx.send(());
        }
        true
    }

    pub(crate) fn claim_native_fallback(&self, prompt_id: u64) -> bool {
        let mut guard = recover(&self.state);
        match &mut *guard {
            QuitGuardState::Prompting {
                prompt_id: current,
                owner,
            } if *current == prompt_id && matches!(owner, PromptOwner::AwaitingAck(_)) => {
                *owner = PromptOwner::NativeFallback;
                true
            }
            _ => false,
        }
    }

    pub(crate) fn resolve_webview(&self, prompt_id: u64, confirm: bool) -> PromptResolution {
        let mut guard = recover(&self.state);
        match *guard {
            QuitGuardState::Prompting {
                prompt_id: current,
                owner: PromptOwner::Webview,
            } if current == prompt_id => {
                *guard = QuitGuardState::Idle;
                if confirm {
                    PromptResolution::Confirmed
                } else {
                    PromptResolution::Cancelled
                }
            }
            _ => PromptResolution::Ignored,
        }
    }

    pub(crate) fn resolve_native_fallback(
        &self,
        prompt_id: u64,
        confirm: bool,
    ) -> PromptResolution {
        let mut guard = recover(&self.state);
        match *guard {
            QuitGuardState::Prompting {
                prompt_id: current,
                owner: PromptOwner::NativeFallback,
            } if current == prompt_id => {
                *guard = QuitGuardState::Idle;
                if confirm {
                    PromptResolution::Confirmed
                } else {
                    PromptResolution::Cancelled
                }
            }
            _ => PromptResolution::Ignored,
        }
    }

    /// Releases a webview-owned prompt when the main window begins reloading.
    /// Leaves `AwaitingAck` untouched so a reload during the ack window falls
    /// back to the native dialog instead of dropping the user's quit, and
    /// leaves `NativeFallback` untouched because the native dialog is independent
    /// of the webview.
    pub(crate) fn cancel_webview_prompt(&self) {
        let mut guard = recover(&self.state);
        if matches!(
            *guard,
            QuitGuardState::Prompting {
                owner: PromptOwner::Webview,
                ..
            }
        ) {
            *guard = QuitGuardState::Idle;
        }
    }

    /// Resets any inspection or prompt state when shutdown begins or the shell
    /// leaves Solo mode.
    pub(crate) fn cancel_all(&self) {
        *recover(&self.state) = QuitGuardState::Idle;
    }
}

/// Formats the plain-text summary shown by the native fallback quit dialog.
pub(crate) fn format_native_quit_message(busy: &SoloBusyWork) -> String {
    let total_tabs = busy.agents.len() + busy.terminals.len();
    let tab_word = if total_tabs == 1 { "tab" } else { "tabs" };
    let mut lines = Vec::with_capacity(total_tabs);

    for agent in &busy.agents {
        let title = if agent.title.is_empty() {
            "Agent"
        } else {
            agent.title.as_str()
        };
        let mut parts = Vec::new();
        let task_count = agent.active_background_tasks.len();
        if task_count > 0 {
            let word = if task_count == 1 {
                "background task"
            } else {
                "background tasks"
            };
            parts.push(format!("{task_count} {word}"));
        }
        let child_count = agent.active_child_agents.len();
        if child_count > 0 {
            let word = if child_count == 1 {
                "child agent"
            } else {
                "child agents"
            };
            parts.push(format!("{child_count} {word}"));
        }
        if parts.is_empty() {
            lines.push(format!("• {title} — agent still working"));
        } else {
            lines.push(format!(
                "• {title} — agent still working ({})",
                parts.join(", ")
            ));
        }
    }

    for term in &busy.terminals {
        let title = if term.title.is_empty() {
            "Terminal"
        } else {
            term.title.as_str()
        };
        let total = usize::try_from(term.total_count)
            .unwrap_or(0)
            .max(term.processes.len());
        let proc_word = if total == 1 { "process" } else { "processes" };
        let shown = term.processes.len().min(3);
        if shown == 0 {
            lines.push(format!("• {title} — {total} {proc_word} running"));
            continue;
        }
        let mut summary = term
            .processes
            .iter()
            .take(shown)
            .map(|proc| {
                if proc.name.is_empty() {
                    "unnamed process"
                } else {
                    proc.name.as_str()
                }
            })
            .collect::<Vec<_>>()
            .join(", ");
        if total > shown {
            summary.push_str(&format!(", and {} more", total - shown));
        }
        lines.push(format!(
            "• {title} — {total} {proc_word} running ({summary})"
        ));
    }

    if lines.is_empty() {
        return format!("Quitting stops work in {total_tabs} {tab_word}:");
    }
    format!(
        "Quitting stops work in {total_tabs} {tab_word}:\n\n{}",
        lines.join("\n")
    )
}

/// Reveals the main window and emits `app:confirm-quit` to the webview, falling
/// back to a native OS confirmation dialog if the webview does not acknowledge
/// within `WEBVIEW_ACK_TIMEOUT`.
pub(crate) fn present_quit_confirmation(shell: Arc<DesktopShell>, busy: SoloBusyWork) {
    // Transition first so a concurrent `cancel_all()` prevents revealing the
    // main window mid-shutdown.
    let Some((prompt_id, ack_rx)) = shell.quit_guard.transition_to_prompt() else {
        return;
    };

    crate::tray::show_main_window(&shell.app_handle);

    let payload = QuitConfirmPayload {
        prompt_id,
        busy_work: busy.clone(),
    };
    if let Err(err) = shell.app_handle.emit_to(
        MAIN_WINDOW_LABEL,
        crate::contracts_generated::EVENT_APP_CONFIRM_QUIT,
        payload,
    ) {
        crate::shell_log!("emit quit confirmation event failed: {err}");
        if shell.quit_guard.claim_native_fallback(prompt_id) {
            show_native_quit_fallback(shell, prompt_id, busy);
        }
        return;
    }

    tauri::async_runtime::spawn(async move {
        if matches!(
            tokio::time::timeout(WEBVIEW_ACK_TIMEOUT, ack_rx).await,
            Ok(Ok(()))
        ) {
            return;
        }
        if shell.quit_guard.claim_native_fallback(prompt_id) {
            show_native_quit_fallback(shell, prompt_id, busy);
        }
    });
}

fn show_native_quit_fallback(shell: Arc<DesktopShell>, prompt_id: u64, busy: SoloBusyWork) {
    let message = format_native_quit_message(&busy);
    let callback_shell = shell.clone();
    // Use non-blocking `.show(...)` rather than `.blocking_show()` so the Tokio
    // worker is never parked waiting for modal UI interaction.
    shell
        .app_handle
        .dialog()
        .message(message)
        .title("Quit LeapMux")
        .kind(MessageDialogKind::Warning)
        .buttons(MessageDialogButtons::OkCancelCustom(
            "Quit anyway".to_string(),
            "Cancel".to_string(),
        ))
        .show(move |confirmed| {
            if callback_shell
                .quit_guard
                .resolve_native_fallback(prompt_id, confirmed)
                == PromptResolution::Confirmed
            {
                crate::handle_app_exit(callback_shell);
            }
        });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quit_guard_state_machine_transitions() {
        let guard = QuitGuard::default();

        // Transitioning to prompt while Idle returns None.
        assert!(guard.transition_to_prompt().is_none());

        // Idle -> Inspecting -> Idle.
        assert_eq!(guard.begin_inspect(), InspectStart::Started);
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyInspecting);
        guard.finish_inspect_idle();
        assert_eq!(guard.begin_inspect(), InspectStart::Started);

        // Inspecting -> Prompting allocates monotonic non-zero prompt IDs.
        let (id1, _rx1) = guard
            .transition_to_prompt()
            .expect("inspecting transitions to prompting");
        assert_eq!(id1, 1);
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyPrompting);

        // finish_inspect_idle leaves Prompting untouched.
        guard.finish_inspect_idle();
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyPrompting);

        // Reset and check monotonic ID increment.
        guard.cancel_all();
        assert_eq!(guard.begin_inspect(), InspectStart::Started);
        let (id2, _rx2) = guard
            .transition_to_prompt()
            .expect("second prompt transitions");
        assert_eq!(id2, 2);
    }

    #[test]
    fn quit_guard_webview_ack_and_native_fallback_are_mutually_exclusive() {
        // (a) Webview claims first.
        let guard = QuitGuard::default();
        assert_eq!(guard.begin_inspect(), InspectStart::Started);
        let (id, mut ack_rx) = guard.transition_to_prompt().expect("prompt started");

        assert!(guard.acknowledge_webview(id));
        assert_eq!(ack_rx.try_recv(), Ok(()));
        assert!(!guard.acknowledge_webview(id));
        assert!(!guard.claim_native_fallback(id));
        assert_eq!(
            guard.resolve_native_fallback(id, true),
            PromptResolution::Ignored
        );
        assert_eq!(guard.resolve_webview(id, true), PromptResolution::Confirmed);
        assert_eq!(guard.begin_inspect(), InspectStart::Started);

        // (b) Native fallback claims first.
        let (id2, _ack_rx2) = guard.transition_to_prompt().expect("second prompt");
        assert!(guard.claim_native_fallback(id2));
        assert!(!guard.acknowledge_webview(id2));
        assert_eq!(guard.resolve_webview(id2, true), PromptResolution::Ignored);
        assert_eq!(
            guard.resolve_native_fallback(id2, false),
            PromptResolution::Cancelled
        );
        assert_eq!(guard.begin_inspect(), InspectStart::Started);
    }

    #[test]
    fn quit_guard_rejects_unacked_stale_and_handles_cancellations() {
        let guard = QuitGuard::default();
        assert_eq!(guard.begin_inspect(), InspectStart::Started);
        let (id, _rx) = guard.transition_to_prompt().expect("prompt started");

        // Resolve before ack is ignored, and wrong prompt IDs fail.
        assert_eq!(guard.resolve_webview(id, true), PromptResolution::Ignored);
        assert!(!guard.acknowledge_webview(id + 99));
        assert!(!guard.claim_native_fallback(id + 99));

        // cancel_webview_prompt leaves AwaitingAck untouched so native fallback still runs.
        guard.cancel_webview_prompt();
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyPrompting);

        // After ack, cancel_webview_prompt resets Webview -> Idle.
        assert!(guard.acknowledge_webview(id));
        assert_eq!(
            guard.resolve_webview(id + 1, true),
            PromptResolution::Ignored
        );
        guard.cancel_webview_prompt();
        assert_eq!(guard.begin_inspect(), InspectStart::Started);

        // NativeFallback stays untouched by cancel_webview_prompt.
        let (id2, _rx2) = guard.transition_to_prompt().expect("second prompt");
        assert!(guard.claim_native_fallback(id2));
        guard.cancel_webview_prompt();
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyPrompting);

        // cancel_all resets both Prompting and Inspecting.
        guard.cancel_all();
        assert_eq!(guard.begin_inspect(), InspectStart::Started);
        guard.cancel_all();
        assert_eq!(guard.begin_inspect(), InspectStart::Started);
    }

    #[test]
    fn solo_busy_work_from_proto_and_is_empty() {
        let empty = SoloBusyWork::from_proto(proto::InspectSoloBusyWorkResponse::default());
        assert!(empty.is_empty());

        let agent_only = SoloBusyWork::from_proto(proto::InspectSoloBusyWorkResponse {
            agents: vec![proto::BusyAgentWork {
                tab_id: "agent-1".to_string(),
                title: "Refactor auth".to_string(),
                active_background_tasks: vec!["Run unit tests".to_string()],
                active_child_agents: vec!["Codebase reviewer".to_string()],
            }],
            terminals: vec![],
        });
        assert!(!agent_only.is_empty());
        assert_eq!(agent_only.agents.len(), 1);
        assert_eq!(agent_only.agents[0].tab_id, "agent-1");
        assert_eq!(agent_only.agents[0].title, "Refactor auth");
        assert_eq!(
            agent_only.agents[0].active_background_tasks,
            vec!["Run unit tests".to_string()]
        );
        assert_eq!(
            agent_only.agents[0].active_child_agents,
            vec!["Codebase reviewer".to_string()]
        );

        let terminal_only = SoloBusyWork::from_proto(proto::InspectSoloBusyWorkResponse {
            agents: vec![],
            terminals: vec![proto::BusyTerminalWork {
                tab_id: "term-1".to_string(),
                title: "Dev server".to_string(),
                processes: vec![proto::BusyTerminalProcess {
                    pid: 51234,
                    name: "node".to_string(),
                }],
                total_count: 3,
            }],
        });
        assert!(!terminal_only.is_empty());
        assert_eq!(terminal_only.terminals.len(), 1);
        assert_eq!(terminal_only.terminals[0].tab_id, "term-1");
        assert_eq!(terminal_only.terminals[0].title, "Dev server");
        assert_eq!(terminal_only.terminals[0].total_count, 3);
        assert_eq!(terminal_only.terminals[0].processes[0].pid, 51234);
        assert_eq!(terminal_only.terminals[0].processes[0].name, "node");

        let mixed = SoloBusyWork::from_proto(proto::InspectSoloBusyWorkResponse {
            agents: vec![proto::BusyAgentWork {
                tab_id: "agent-1".to_string(),
                title: "Agent A".to_string(),
                active_background_tasks: vec![],
                active_child_agents: vec![],
            }],
            terminals: vec![proto::BusyTerminalWork {
                tab_id: "term-1".to_string(),
                title: "Term A".to_string(),
                processes: vec![proto::BusyTerminalProcess {
                    pid: 1,
                    name: "sleep".to_string(),
                }],
                total_count: 1,
            }],
        });
        assert!(!mixed.is_empty());
        assert_eq!(mixed.agents.len(), 1);
        assert_eq!(mixed.agents[0].title, "Agent A");
        assert_eq!(mixed.terminals.len(), 1);
        assert_eq!(mixed.terminals[0].title, "Term A");
    }

    #[test]
    fn format_native_quit_message_covers_singular_plural_and_overflow() {
        let single_agent = SoloBusyWork {
            agents: vec![BusyAgentWork {
                tab_id: "a1".to_string(),
                title: "Solo agent".to_string(),
                active_background_tasks: vec![],
                active_child_agents: vec![],
            }],
            terminals: vec![],
        };
        assert_eq!(
            format_native_quit_message(&single_agent),
            "Quitting stops work in 1 tab:\n\n• Solo agent — agent still working"
        );

        let mixed = SoloBusyWork {
            agents: vec![BusyAgentWork {
                tab_id: "a1".to_string(),
                title: "Refactor auth".to_string(),
                active_background_tasks: vec!["Run unit tests".to_string()],
                active_child_agents: vec!["Reviewer 1".to_string(), "Reviewer 2".to_string()],
            }],
            terminals: vec![
                BusyTerminalWork {
                    tab_id: "t1".to_string(),
                    title: "Single proc".to_string(),
                    processes: vec![BusyTerminalProcess {
                        pid: 100,
                        name: "vite".to_string(),
                    }],
                    total_count: 1,
                },
                BusyTerminalWork {
                    tab_id: "t2".to_string(),
                    title: "Unnamed proc".to_string(),
                    processes: vec![BusyTerminalProcess {
                        pid: 101,
                        name: String::new(),
                    }],
                    total_count: 1,
                },
                BusyTerminalWork {
                    tab_id: "t3".to_string(),
                    title: "Build farm".to_string(),
                    processes: vec![
                        BusyTerminalProcess {
                            pid: 201,
                            name: "cc".to_string(),
                        },
                        BusyTerminalProcess {
                            pid: 202,
                            name: "ld".to_string(),
                        },
                        BusyTerminalProcess {
                            pid: 203,
                            name: "rustc".to_string(),
                        },
                        BusyTerminalProcess {
                            pid: 204,
                            name: "llvm".to_string(),
                        },
                    ],
                    total_count: 10,
                },
            ],
        };
        assert_eq!(
            format_native_quit_message(&mixed),
            concat!(
                "Quitting stops work in 4 tabs:\n\n",
                "• Refactor auth — agent still working (1 background task, 2 child agents)\n",
                "• Single proc — 1 process running (vite)\n",
                "• Unnamed proc — 1 process running (unnamed process)\n",
                "• Build farm — 10 processes running (cc, ld, rustc, and 7 more)",
            )
        );
    }
}
