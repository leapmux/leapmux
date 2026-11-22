use std::sync::{
    atomic::{AtomicU64, Ordering},
    Arc, Mutex,
};
use std::time::Duration;

use serde::Serialize;
use tauri::{Emitter, Manager};
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
pub(crate) enum StopIntent {
    Quit,
    SwitchMode,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum InspectStart {
    Started(u64),
    AlreadyInspecting,
    AlreadyPrompting,
    SwitchingInProgress,
    AlreadyExiting,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SwitchInspectStart {
    Inspect(u64),
    Proceed(u64),
    Blocked,
    BlockedPrompting,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(tag = "kind", rename_all = "camelCase")]
pub(crate) enum BeginSwitchModeResult {
    #[serde(rename_all = "camelCase")]
    Proceed {
        switch_id: u64,
    },
    #[serde(rename_all = "camelCase")]
    Confirm {
        prompt_id: u64,
        busy_work: SoloBusyWork,
    },
    Blocked,
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
    Inspecting {
        inspect_id: u64,
        intent: StopIntent,
    },
    Prompting {
        prompt_id: u64,
        intent: StopIntent,
        owner: PromptOwner,
    },
    Switching {
        switch_id: u64,
    },
    Exiting,
}

/// Coordinates solo-mode busy-work inspection, single-flight quit and
/// switch-mode confirmation across the webview and native fallback dialogs,
/// switch-mode authorization tokens, and the one-shot exit latch.
pub(crate) struct QuitGuard {
    next_inspect_id: AtomicU64,
    next_prompt_id: AtomicU64,
    next_switch_id: AtomicU64,
    state: Mutex<QuitGuardState>,
}

impl Default for QuitGuard {
    fn default() -> Self {
        Self {
            next_inspect_id: AtomicU64::new(1),
            next_prompt_id: AtomicU64::new(1),
            next_switch_id: AtomicU64::new(1),
            state: Mutex::new(QuitGuardState::Idle),
        }
    }
}

impl QuitGuard {
    /// Atomically claims the process exit once across all quit routes,
    /// discarding any in-flight inspection, confirmation prompt, or switch.
    pub(crate) fn claim_exit(&self) -> bool {
        let mut guard = recover(&self.state);
        if matches!(*guard, QuitGuardState::Exiting) {
            return false;
        }
        *guard = QuitGuardState::Exiting;
        true
    }

    pub(crate) fn is_exiting(&self) -> bool {
        matches!(*recover(&self.state), QuitGuardState::Exiting)
    }

    /// Starts a quit busy-work inspection. Supersedes any in-flight
    /// `SwitchMode` inspection or prompt so quitting always takes precedence.
    /// Reports `SwitchingInProgress` when the user already confirmed switching
    /// modes so quitting proceeds without prompting a second time.
    pub(crate) fn begin_inspect(&self) -> InspectStart {
        let mut guard = recover(&self.state);
        match *guard {
            QuitGuardState::Idle
            | QuitGuardState::Inspecting {
                intent: StopIntent::SwitchMode,
                ..
            }
            | QuitGuardState::Prompting {
                intent: StopIntent::SwitchMode,
                ..
            } => {
                let inspect_id = self.next_inspect_id.fetch_add(1, Ordering::Relaxed);
                *guard = QuitGuardState::Inspecting {
                    inspect_id,
                    intent: StopIntent::Quit,
                };
                InspectStart::Started(inspect_id)
            }
            QuitGuardState::Inspecting {
                intent: StopIntent::Quit,
                ..
            } => InspectStart::AlreadyInspecting,
            QuitGuardState::Prompting {
                intent: StopIntent::Quit,
                ..
            } => InspectStart::AlreadyPrompting,
            QuitGuardState::Switching { .. } => InspectStart::SwitchingInProgress,
            QuitGuardState::Exiting => InspectStart::AlreadyExiting,
        }
    }

    pub(crate) fn is_inspecting(&self, inspect_id: u64) -> bool {
        matches!(
            *recover(&self.state),
            QuitGuardState::Inspecting {
                inspect_id: current,
                intent: StopIntent::Quit,
            } if current == inspect_id
        )
    }

    pub(crate) fn finish_inspect_idle(&self, inspect_id: u64) -> bool {
        let mut guard = recover(&self.state);
        if matches!(
            *guard,
            QuitGuardState::Inspecting {
                inspect_id: current,
                intent: StopIntent::Quit,
            } if current == inspect_id
        ) {
            *guard = QuitGuardState::Idle;
            return true;
        }
        false
    }

    pub(crate) fn transition_to_prompt(
        &self,
        inspect_id: u64,
    ) -> Option<(u64, oneshot::Receiver<()>)> {
        let mut guard = recover(&self.state);
        if !matches!(
            *guard,
            QuitGuardState::Inspecting {
                inspect_id: current,
                intent: StopIntent::Quit,
            } if current == inspect_id
        ) {
            return None;
        }
        let prompt_id = self.next_prompt_id.fetch_add(1, Ordering::Relaxed);
        let (ack_tx, ack_rx) = oneshot::channel();
        *guard = QuitGuardState::Prompting {
            prompt_id,
            intent: StopIntent::Quit,
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
                intent: StopIntent::Quit,
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
                intent: StopIntent::Quit,
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
                intent: StopIntent::Quit,
                owner,
            } if *current == prompt_id && matches!(owner, PromptOwner::AwaitingAck(_)) => {
                *owner = PromptOwner::NativeFallback;
                true
            }
            _ => false,
        }
    }

    fn resolve_owned(
        &self,
        prompt_id: u64,
        confirm: bool,
        matches_owner: impl FnOnce(&PromptOwner) -> bool,
    ) -> PromptResolution {
        let mut guard = recover(&self.state);
        match &*guard {
            QuitGuardState::Prompting {
                prompt_id: current,
                intent: StopIntent::Quit,
                owner,
            } if *current == prompt_id && matches_owner(owner) => {
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

    pub(crate) fn resolve_webview(&self, prompt_id: u64, confirm: bool) -> PromptResolution {
        self.resolve_owned(prompt_id, confirm, |owner| {
            matches!(owner, PromptOwner::Webview)
        })
    }

    pub(crate) fn resolve_native_fallback(
        &self,
        prompt_id: u64,
        confirm: bool,
    ) -> PromptResolution {
        self.resolve_owned(prompt_id, confirm, |owner| {
            matches!(owner, PromptOwner::NativeFallback)
        })
    }

    /// Starts a coordinated Switch Mode check. In connected Solo mode, claims
    /// an inspection slot; otherwise issues an immediate switch token.
    pub(crate) fn begin_switch_inspect(&self, is_solo_connected: bool) -> SwitchInspectStart {
        let mut guard = recover(&self.state);
        match *guard {
            QuitGuardState::Idle => {
                if is_solo_connected {
                    let inspect_id = self.next_inspect_id.fetch_add(1, Ordering::Relaxed);
                    *guard = QuitGuardState::Inspecting {
                        inspect_id,
                        intent: StopIntent::SwitchMode,
                    };
                    SwitchInspectStart::Inspect(inspect_id)
                } else {
                    let switch_id = self.next_switch_id.fetch_add(1, Ordering::Relaxed);
                    *guard = QuitGuardState::Switching { switch_id };
                    SwitchInspectStart::Proceed(switch_id)
                }
            }
            QuitGuardState::Prompting {
                intent: StopIntent::Quit,
                ..
            } => SwitchInspectStart::BlockedPrompting,
            QuitGuardState::Inspecting { .. }
            | QuitGuardState::Prompting {
                intent: StopIntent::SwitchMode,
                ..
            }
            | QuitGuardState::Switching { .. }
            | QuitGuardState::Exiting => SwitchInspectStart::Blocked,
        }
    }

    /// Releases an in-flight Switch Mode inspection if its task is dropped before
    /// completion. Leaves superseded or exiting states untouched.
    pub(crate) fn cancel_switch_inspect(&self, inspect_id: u64) {
        let mut guard = recover(&self.state);
        if matches!(
            *guard,
            QuitGuardState::Inspecting {
                inspect_id: current,
                intent: StopIntent::SwitchMode,
            } if current == inspect_id
        ) {
            *guard = QuitGuardState::Idle;
        }
    }

    /// Completes a Switch Mode busy-work inspection, either issuing a switch
    /// token immediately (idle or disconnected) or opening a Switch Mode prompt.
    pub(crate) fn complete_switch_inspect(
        &self,
        inspect_id: u64,
        is_solo_connected: bool,
        busy: SoloBusyWork,
    ) -> BeginSwitchModeResult {
        let mut guard = recover(&self.state);
        if !matches!(
            *guard,
            QuitGuardState::Inspecting {
                inspect_id: current,
                intent: StopIntent::SwitchMode,
            } if current == inspect_id
        ) {
            return BeginSwitchModeResult::Blocked;
        }
        if !is_solo_connected || busy.is_empty() {
            let switch_id = self.next_switch_id.fetch_add(1, Ordering::Relaxed);
            *guard = QuitGuardState::Switching { switch_id };
            return BeginSwitchModeResult::Proceed { switch_id };
        }
        let prompt_id = self.next_prompt_id.fetch_add(1, Ordering::Relaxed);
        *guard = QuitGuardState::Prompting {
            prompt_id,
            intent: StopIntent::SwitchMode,
            owner: PromptOwner::Webview,
        };
        BeginSwitchModeResult::Confirm {
            prompt_id,
            busy_work: busy,
        }
    }

    /// Resolves an open Switch Mode confirmation prompt. Returns a switch token
    /// when confirmed so the frontend can complete `switch_mode` after its fade.
    pub(crate) fn resolve_switch_prompt(&self, prompt_id: u64, confirm: bool) -> Option<u64> {
        let mut guard = recover(&self.state);
        if !matches!(
            *guard,
            QuitGuardState::Prompting {
                prompt_id: current,
                intent: StopIntent::SwitchMode,
                owner: PromptOwner::Webview,
            } if current == prompt_id
        ) {
            return None;
        }
        if confirm {
            let switch_id = self.next_switch_id.fetch_add(1, Ordering::Relaxed);
            *guard = QuitGuardState::Switching { switch_id };
            Some(switch_id)
        } else {
            *guard = QuitGuardState::Idle;
            None
        }
    }

    /// Releases a `Switching` token when the frontend aborts before invoking
    /// `switch_mode`, or after `switch_mode` finishes. Preserves `Exiting`.
    pub(crate) fn finish_switch(&self, switch_id: u64) {
        let mut guard = recover(&self.state);
        if matches!(
            *guard,
            QuitGuardState::Switching { switch_id: current } if current == switch_id
        ) {
            *guard = QuitGuardState::Idle;
        }
    }

    /// Validates that `switch_mode` is allowed to run. In connected Solo mode,
    /// requires the active `switch_id` token issued by `begin_switch_mode` or
    /// `resolve_switch_mode_confirmation`. Outside Solo mode, accepts either a
    /// matching token or an unguarded `None` while `Idle` (e.g. launcher or
    /// distributed sign-in screens) and claims a temporary `Switching` token.
    pub(crate) fn enter_switch_mode(
        &self,
        is_solo_connected: bool,
        switch_id: Option<u64>,
    ) -> Result<u64, String> {
        let mut guard = recover(&self.state);
        if matches!(*guard, QuitGuardState::Exiting) {
            return Err("application is exiting".to_string());
        }
        if let Some(requested_id) = switch_id {
            if matches!(
                *guard,
                QuitGuardState::Switching { switch_id: current } if current == requested_id
            ) {
                return Ok(requested_id);
            }
            return Err("switch mode is not authorized".to_string());
        }
        if is_solo_connected {
            return Err("switch mode requires busy-work confirmation in Solo mode".to_string());
        }
        if matches!(*guard, QuitGuardState::Idle) {
            let token = self.next_switch_id.fetch_add(1, Ordering::Relaxed);
            *guard = QuitGuardState::Switching { switch_id: token };
            return Ok(token);
        }
        Err("switch mode is blocked by another transition".to_string())
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

    /// Resets any in-flight inspection, prompt, or switch state when leaving
    /// Solo mode. Preserves `Exiting` so a late cleanup cannot un-latch an
    /// active exit.
    pub(crate) fn cancel_all(&self) {
        let mut guard = recover(&self.state);
        if !matches!(*guard, QuitGuardState::Exiting) {
            *guard = QuitGuardState::Idle;
        }
    }
}

/// Formats the plain-text summary shown by the native fallback quit dialog.
pub(crate) fn format_native_quit_message(busy: &SoloBusyWork) -> String {
    fn pluralize<'a>(count: usize, singular: &'a str, plural: &'a str) -> &'a str {
        if count == 1 {
            singular
        } else {
            plural
        }
    }

    let total_tabs = busy.agents.len() + busy.terminals.len();
    let tab_word = pluralize(total_tabs, "tab", "tabs");
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
            let word = pluralize(task_count, "background task", "background tasks");
            parts.push(format!("{task_count} {word}"));
        }
        let child_count = agent.active_child_agents.len();
        if child_count > 0 {
            let word = pluralize(child_count, "child agent", "child agents");
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
        let proc_word = pluralize(total, "process", "processes");
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
pub(crate) fn present_quit_confirmation(
    shell: Arc<DesktopShell>,
    inspect_id: u64,
    busy: SoloBusyWork,
) {
    // Transition first so a concurrent `cancel_all()` or `claim_exit()`
    // prevents revealing the main window mid-transition.
    let Some((prompt_id, ack_rx)) = shell.quit_guard.transition_to_prompt(inspect_id) else {
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
    // worker is never parked waiting for modal UI interaction. Parent to the
    // main window when available so re-focusing the main window on repeated
    // quit attempts never raises it over the native alert.
    let mut dialog = shell
        .app_handle
        .dialog()
        .message(message)
        .title("Quit LeapMux")
        .kind(MessageDialogKind::Warning)
        .buttons(MessageDialogButtons::OkCancelCustom(
            "Quit anyway".to_string(),
            "Cancel".to_string(),
        ));
    if let Some(window) = shell.app_handle.get_webview_window(MAIN_WINDOW_LABEL) {
        dialog = dialog.parent(&window);
    }
    dialog.show(move |confirmed| {
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
        assert!(guard.transition_to_prompt(1).is_none());

        // Idle -> Inspecting -> Idle with matching inspect_id.
        let InspectStart::Started(inspect1) = guard.begin_inspect() else {
            panic!("expected first inspect to start");
        };
        assert_eq!(inspect1, 1);
        assert!(guard.is_inspecting(inspect1));
        assert!(!guard.is_inspecting(inspect1 + 1));
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyInspecting);
        assert!(!guard.finish_inspect_idle(inspect1 + 1));
        assert!(guard.finish_inspect_idle(inspect1));

        let InspectStart::Started(inspect2) = guard.begin_inspect() else {
            panic!("expected second inspect to start");
        };
        assert_eq!(inspect2, 2);

        // Stale inspect_id after cancel_all + newer inspect cannot clear or hijack the newer inspect.
        guard.cancel_all();
        let InspectStart::Started(inspect3) = guard.begin_inspect() else {
            panic!("expected third inspect to start");
        };
        assert_eq!(inspect3, 3);
        assert!(!guard.finish_inspect_idle(inspect2));
        assert!(guard.transition_to_prompt(inspect2).is_none());
        assert!(guard.is_inspecting(inspect3));

        // Inspecting -> Prompting allocates monotonic non-zero prompt IDs.
        let (id1, _rx1) = guard
            .transition_to_prompt(inspect3)
            .expect("inspecting transitions to prompting");
        assert_eq!(id1, 1);
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyPrompting);

        // finish_inspect_idle leaves Prompting untouched.
        assert!(!guard.finish_inspect_idle(inspect3));
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyPrompting);

        // Reset and check monotonic ID increment.
        guard.cancel_all();
        let InspectStart::Started(inspect4) = guard.begin_inspect() else {
            panic!("expected fourth inspect to start");
        };
        let (id2, _rx2) = guard
            .transition_to_prompt(inspect4)
            .expect("second prompt transitions");
        assert_eq!(id2, 2);

        // claim_exit latches Exiting once and cancel_all preserves Exiting.
        assert!(!guard.is_exiting());
        assert!(guard.claim_exit());
        assert!(guard.is_exiting());
        assert!(!guard.claim_exit());
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyExiting);
        guard.cancel_all();
        assert!(guard.is_exiting());
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyExiting);
    }

    #[test]
    fn quit_guard_webview_ack_and_native_fallback_are_mutually_exclusive() {
        // (a) Webview claims first.
        let guard = QuitGuard::default();
        let InspectStart::Started(inspect1) = guard.begin_inspect() else {
            panic!("expected inspect1");
        };
        let (id, mut ack_rx) = guard
            .transition_to_prompt(inspect1)
            .expect("prompt started");

        assert!(guard.acknowledge_webview(id));
        assert_eq!(ack_rx.try_recv(), Ok(()));
        assert!(!guard.acknowledge_webview(id));
        assert!(!guard.claim_native_fallback(id));
        assert_eq!(
            guard.resolve_native_fallback(id, true),
            PromptResolution::Ignored
        );
        assert_eq!(guard.resolve_webview(id, true), PromptResolution::Confirmed);
        let InspectStart::Started(inspect2) = guard.begin_inspect() else {
            panic!("expected inspect2");
        };

        // (b) Native fallback claims first.
        let (id2, _ack_rx2) = guard.transition_to_prompt(inspect2).expect("second prompt");
        assert!(guard.claim_native_fallback(id2));
        assert!(!guard.acknowledge_webview(id2));
        assert_eq!(guard.resolve_webview(id2, true), PromptResolution::Ignored);
        assert_eq!(
            guard.resolve_native_fallback(id2, false),
            PromptResolution::Cancelled
        );
        assert!(matches!(guard.begin_inspect(), InspectStart::Started(_)));
    }

    #[test]
    fn quit_guard_rejects_unacked_stale_and_handles_cancellations() {
        let guard = QuitGuard::default();
        let InspectStart::Started(inspect1) = guard.begin_inspect() else {
            panic!("expected inspect1");
        };
        let (id, _rx) = guard
            .transition_to_prompt(inspect1)
            .expect("prompt started");

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
        let InspectStart::Started(inspect2) = guard.begin_inspect() else {
            panic!("expected inspect2");
        };

        // NativeFallback stays untouched by cancel_webview_prompt.
        let (id2, _rx2) = guard.transition_to_prompt(inspect2).expect("second prompt");
        assert!(guard.claim_native_fallback(id2));
        guard.cancel_webview_prompt();
        assert_eq!(guard.begin_inspect(), InspectStart::AlreadyPrompting);

        // cancel_all resets both Prompting and Inspecting.
        guard.cancel_all();
        assert!(matches!(guard.begin_inspect(), InspectStart::Started(_)));
        guard.cancel_all();
        assert!(matches!(guard.begin_inspect(), InspectStart::Started(_)));
    }

    #[test]
    fn quit_guard_coordinates_switch_mode_tokens_and_quit_precedence() {
        let busy = SoloBusyWork {
            agents: vec![BusyAgentWork {
                tab_id: "a1".to_string(),
                title: "Agent".to_string(),
                active_background_tasks: vec![],
                active_child_agents: vec![],
            }],
            terminals: vec![],
        };

        // 1. Non-Solo immediate proceed + token validation + finish_switch reset.
        let guard = QuitGuard::default();
        let SwitchInspectStart::Proceed(sw1) = guard.begin_switch_inspect(false) else {
            panic!("expected immediate proceed outside Solo");
        };
        assert_eq!(
            guard.begin_switch_inspect(false),
            SwitchInspectStart::Blocked
        );
        assert!(guard.enter_switch_mode(false, Some(sw1 + 1)).is_err());
        assert_eq!(guard.enter_switch_mode(false, Some(sw1)), Ok(sw1));
        guard.finish_switch(sw1);
        // Outside Solo while Idle, None is allowed (e.g. BrowserSignIn) and claims a token.
        let sw_unprompted = guard
            .enter_switch_mode(false, None)
            .expect("non-Solo None allowed when Idle");
        assert!(guard.enter_switch_mode(false, None).is_err());
        guard.finish_switch(sw_unprompted);

        // 2. Solo connected rejects unguarded switch_mode(None).
        assert!(guard.enter_switch_mode(true, None).is_err());

        // 3. Solo connected -> Inspect -> Confirm -> Resolve(true) -> Switching -> finish_switch.
        let SwitchInspectStart::Inspect(insp1) = guard.begin_switch_inspect(true) else {
            panic!("expected Solo switch inspect");
        };
        assert_eq!(
            guard.begin_switch_inspect(true),
            SwitchInspectStart::Blocked
        );
        let BeginSwitchModeResult::Confirm {
            prompt_id,
            busy_work,
        } = guard.complete_switch_inspect(insp1, true, busy.clone())
        else {
            panic!("expected Confirm");
        };
        assert_eq!(busy_work, busy);
        assert_eq!(guard.resolve_switch_prompt(prompt_id + 1, true), None);
        let sw2 = guard
            .resolve_switch_prompt(prompt_id, true)
            .expect("confirm yields switch_id");
        assert_eq!(guard.enter_switch_mode(true, Some(sw2)), Ok(sw2));
        // Quit while Switching returns SwitchingInProgress (exit without double-prompting).
        assert_eq!(guard.begin_inspect(), InspectStart::SwitchingInProgress);
        guard.finish_switch(sw2);

        // 4. Quit supersedes SwitchMode during Inspecting and during Prompting.
        let SwitchInspectStart::Inspect(insp2) = guard.begin_switch_inspect(true) else {
            panic!("expected switch inspect 2");
        };
        let InspectStart::Started(quit_insp1) = guard.begin_inspect() else {
            panic!("quit must supersede switch inspect");
        };
        assert_eq!(
            guard.complete_switch_inspect(insp2, true, busy.clone()),
            BeginSwitchModeResult::Blocked
        );
        assert!(guard.finish_inspect_idle(quit_insp1));

        let SwitchInspectStart::Inspect(insp3) = guard.begin_switch_inspect(true) else {
            panic!("expected switch inspect 3");
        };
        let BeginSwitchModeResult::Confirm {
            prompt_id: switch_prompt,
            ..
        } = guard.complete_switch_inspect(insp3, true, busy.clone())
        else {
            panic!("expected switch confirm");
        };
        let InspectStart::Started(quit_insp2) = guard.begin_inspect() else {
            panic!("quit must supersede switch prompt");
        };
        assert_eq!(
            guard.resolve_switch_prompt(switch_prompt, true),
            None,
            "superseded switch prompt cannot authorize switch_mode"
        );
        let (_quit_prompt, _rx) = guard
            .transition_to_prompt(quit_insp2)
            .expect("quit inspect transitions to quit prompt");
        assert_eq!(
            guard.begin_switch_inspect(true),
            SwitchInspectStart::BlockedPrompting
        );

        // 5. cancel_switch_inspect resets SwitchMode inspection without affecting Quit,
        // and cancel_webview_prompt clears an open SwitchMode prompt.
        guard.cancel_all();
        let SwitchInspectStart::Inspect(insp4) = guard.begin_switch_inspect(true) else {
            panic!("expected switch inspect 4");
        };
        guard.cancel_switch_inspect(insp4);
        assert!(matches!(
            guard.begin_switch_inspect(true),
            SwitchInspectStart::Inspect(_)
        ));
        guard.cancel_all();
        let SwitchInspectStart::Inspect(insp5) = guard.begin_switch_inspect(true) else {
            panic!("expected switch inspect 5");
        };
        let BeginSwitchModeResult::Confirm {
            prompt_id: p_reload,
            ..
        } = guard.complete_switch_inspect(insp5, true, busy)
        else {
            panic!("expected switch confirm for reload");
        };
        guard.cancel_webview_prompt();
        assert_eq!(guard.resolve_switch_prompt(p_reload, true), None);

        // 6. Claiming exit blocks enter_switch_mode even with a previously issued token.
        guard.cancel_all();
        let SwitchInspectStart::Proceed(sw3) = guard.begin_switch_inspect(false) else {
            panic!("expected sw3");
        };
        assert!(guard.claim_exit());
        assert!(guard.enter_switch_mode(false, Some(sw3)).is_err());
        assert!(guard.enter_switch_mode(true, Some(sw3)).is_err());
        assert_eq!(
            guard.begin_switch_inspect(true),
            SwitchInspectStart::Blocked
        );
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
