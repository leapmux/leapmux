//! macOS: route every `terminate:` into the cooperative shutdown.
//!
//! The Quit item of the macOS menu bar is `PredefinedMenuItem::quit`, whose
//! action IS `-[NSApplication terminate:]`. The Dock's Quit, Cmd+Q,
//! an AppleScript `quit`, and a logout reach the same selector. tao
//! implements `applicationWillTerminate:` and not `applicationShouldTerminate:`,
//! and its wry runtime maps the `terminate:` path onto Tauri's `RunEvent::Exit`,
//! never onto `ExitRequested` -- so the cooperative shutdown that main.rs
//! arms for `ExitRequested` never runs, the shell leaves at once, and the
//! sidecar loses the Hub's runtime lease. One override on the delegate's
//! class covers every one of those routes, for the same reason one override
//! of `miniaturize:` covers every minimize (see tray/minimize_macos.rs, whose
//! install shape this module follows).
//!
//! The hook answers `NSTerminateLater` while `begin_app_exit` drains, and the
//! `finish` closure of that call sends `replyToApplicationShouldTerminate:YES`
//! on the main thread, which commits the quit AppKit is holding. An exit that
//! another route (`quit_app`, the tray) already started answers
//! `NSTerminateCancel`: the running exit still ends the application, and a
//! logout that landed in the same seconds is left for the user to repeat.
//!
//! Two things limit the hold. A SECOND `terminate:` during `NSTerminateLater`
//! skips the delegate and ends the process at once; that is AppKit's own
//! rule, and the sidecar survives the loss of the shell, so the shutdown
//! still finishes. And a panic inside the claim answers `NSTerminateNow`:
//! a quit must not be refused, and the sidecar finishes its drain by itself
//! through the lost-stdio protection installed at startup.
//!
//! The method goes on the class the delegate was CREATED as (`-class`), and
//! never on the live class, so a Key-Value Observing subclass that AppKit
//! generates and later removes cannot take the override with it.

use std::ffi::CStr;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::ptr::{self, NonNull};
use std::sync::{Arc, OnceLock};

use objc2::ffi::class_addMethod;
use objc2::runtime::{AnyClass, AnyObject, Imp, Sel};
use objc2::{msg_send, sel};

use super::{begin_app_exit, DesktopShell};

/// The Objective-C type encoding of `-[NSApplication
/// applicationShouldTerminate:]`: an `NSUInteger` return (`NSTerminateReply`),
/// an object receiver, the selector, and one object argument.
///
/// Spelled out because the method goes on a class that already exists, which
/// `class_addMethod` takes an encoding for. See
/// tray/minimize_macos.rs for why no class builder can derive it here.
const TERMINATE_TYPES: &CStr = c"Q@:@";

/// AppKit's `NSApplicationTerminateReply` values.
///
/// `NSTerminateCancel`: the delegate refused; the running exit still ends the
/// application. `NSTerminateNow`: proceed with the ordinary terminate.
/// `NSTerminateLater`: hold the quit open; a later
/// `replyToApplicationShouldTerminate:YES` commits it.
const NSTERMINATE_CANCEL: usize = 0;
const NSTERMINATE_NOW: usize = 1;
const NSTERMINATE_LATER: usize = 2;

/// What the hook needs to answer a `terminate:`.
///
/// A process global, because what it describes is one. `install` returns
/// before it publishes a second hook, so the field always describes the one
/// delegate class that it hooked.
struct TerminateHook {
    /// Claims the exit through `begin_app_exit`. True means this hook holds
    /// the quit; false means an exit that another route started already runs.
    ///
    /// A closure over the `DesktopShell` that `install` received, so the hook
    /// acts on the shell it was given. It is also the seam the tests drive: a
    /// `DesktopShell` needs a running application, and a stub claim does not.
    claim: Box<dyn Fn() -> bool + Send + Sync>,
}

static HOOK: OnceLock<TerminateHook> = OnceLock::new();

/// Install the `applicationShouldTerminate:` hook on the delegate's class.
///
/// The `Err` cases each state the quit behaviour the shell is left with, and
/// `setup` logs them with `shell_log!`. Silence would bring the bug back: a
/// future tao that implements the selector makes `class_addMethod` return
/// false, and an unlogged refusal would look like a working hook.
pub(crate) fn install(shell: Arc<DesktopShell>) -> Result<(), String> {
    let Some(delegate) = shared_app_delegate() else {
        return Err(
            "NSApplication has no delegate; a quit cannot run the cooperative shutdown".to_string(),
        );
    };
    // `-class`, and not the live class: a Key-Value Observing subclass that
    // AppKit generates and later removes would take the hook with it. See the
    // module documentation.
    // SAFETY: `-class` takes no argument and answers a class object, and every
    // registered class lives for the length of the process.
    let target: &'static AnyClass = unsafe {
        let class: *const AnyClass = msg_send![delegate, class];
        class.as_ref().ok_or_else(|| {
            "the delegate answered no class; a quit cannot run the cooperative shutdown".to_string()
        })?
    };
    // The hook is published BEFORE the method goes on the class, so a
    // `terminate:` that reaches the override never finds the hook missing.
    if HOOK
        .set(TerminateHook {
            claim: Box::new(move || {
                begin_app_exit(shell.clone(), reply_to_terminate_after_shutdown)
            }),
        })
        .is_err()
    {
        return Err("the terminate hook is already published; this install is dropped".to_string());
    }
    add_hook(target)
}

/// The delegate of the running NSApplication, if there is one.
fn shared_app_delegate() -> Option<&'static AnyObject> {
    let app = shared_application()?;
    // SAFETY: `delegate` answers the object AppKit keeps for the length of
    // the process.
    let delegate: *mut AnyObject = unsafe { msg_send![app, delegate] };
    // SAFETY: the delegate lives for the length of the process, so the borrow
    // outlives every use.
    let delegate = NonNull::new(delegate)?;
    Some(unsafe { delegate.as_ref() })
}

/// Whether `class` defines `applicationShouldTerminate:` ITSELF.
///
/// The check reads the class's own method list (`class_copyMethodList`),
/// because that is the exact condition under which the runtime refuses
/// `class_addMethod`: a method the class itself defines blocks the add, and
/// one it merely inherits does not.
fn defines_terminate(class: &AnyClass) -> bool {
    class
        .instance_methods()
        .iter()
        .any(|method| method.name() == sel!(applicationShouldTerminate:))
}

/// Add the `applicationShouldTerminate:` hook to `class`.
///
/// The runtime refuses `class_addMethod` for a class that defines the method
/// already, which is the "a future tao implements the selector" case, and for
/// an unregistered class, which cannot happen here.
fn add_hook(class: &AnyClass) -> Result<(), String> {
    if defines_terminate(class) {
        return Err(format!(
            "{} already answers applicationShouldTerminate:; a quit bypasses the cooperative shutdown",
            class.name().to_string_lossy()
        ));
    }
    // SAFETY: `class` is a registered class, and `TERMINATE_TYPES` states the
    // arguments and the return value that `applicationShouldTerminate` takes.
    let accepted = unsafe {
        class_addMethod(
            ptr::from_ref(class).cast_mut(),
            sel!(applicationShouldTerminate:),
            terminate_imp(),
            TERMINATE_TYPES.as_ptr(),
        )
    }
    .as_bool();
    if accepted {
        Ok(())
    } else {
        Err(format!(
            "the runtime refused the applicationShouldTerminate: hook on {}; \
             a quit bypasses the cooperative shutdown",
            class.name().to_string_lossy()
        ))
    }
}

/// The hook, as the untyped function pointer that `class_addMethod` takes.
///
/// objc2 builds one from a typed function inside its class builder, and that
/// path is closed here: a builder makes a new class, and this method goes on
/// a class that exists.
fn terminate_imp() -> Imp {
    // SAFETY: `Imp` is the same `extern "C-unwind"` function pointer with its
    // signature erased. The runtime calls it with the arguments that
    // `TERMINATE_TYPES` declares, and those are the ones
    // `application_should_terminate` takes.
    unsafe {
        std::mem::transmute::<extern "C-unwind" fn(&AnyObject, Sel, *mut AnyObject) -> usize, Imp>(
            application_should_terminate,
        )
    }
}

/// `-[NSObject applicationShouldTerminate:]` on tao's delegate class.
extern "C-unwind" fn application_should_terminate(
    _this: &AnyObject,
    _cmd: Sel,
    _sender: *mut AnyObject,
) -> usize {
    let Some(hook) = HOOK.get() else {
        // Impossible: `install` publishes the hook before it adds the method,
        // and a `OnceLock` never empties. A quit must not be refused, so the
        // ordinary terminate proceeds.
        crate::shell_log!("a terminate arrived before the hook was published; it proceeds now");
        return NSTERMINATE_NOW;
    };
    terminate_reply(&*hook.claim)
}

/// The whole decision of a `terminate:`: claim the exit, or answer for the
/// exit that another route already started, or answer for a panic.
///
/// Pure, so the tests drive it without the Objective-C machinery, and so the
/// hook stays a two-line dispatch. The `catch_unwind` keeps a panic out of the
/// AppKit frame that sent `terminate:` -- tao installs the same barrier at its
/// own callbacks (`stop_app_on_panic`). A panic answers `NSTerminateNow`
/// rather than `NSTerminateCancel`, because a quit must not be refused: the
/// shell leaves at once, and the sidecar finishes its drain by itself.
fn terminate_reply(claim: &dyn Fn() -> bool) -> usize {
    match catch_unwind(AssertUnwindSafe(claim)) {
        Ok(true) => NSTERMINATE_LATER,
        Ok(false) => NSTERMINATE_CANCEL,
        Err(_) => {
            crate::shell_log!(
                "the quit claim panicked; the app terminates now and the sidecar finishes by itself"
            );
            NSTERMINATE_NOW
        }
    }
}

/// Commit the quit that `NSTerminateLater` is holding.
///
/// AppKit requires `replyToApplicationShouldTerminate:YES` on the MAIN thread
/// before it proceeds past a delegate that answered `NSTerminateLater`, so the
/// reply is scheduled there. The tao event-loop proxy wakes the main thread
/// during the hold, which a probe confirmed.
fn reply_to_terminate_after_shutdown(app: &crate::AppHandle) {
    let scheduled = app.run_on_main_thread(|| {
        let Some(ns_app) = shared_application() else {
            crate::shell_log!("NSApplication is gone; the quit reply is lost");
            return;
        };
        // SAFETY: the reply goes to the application that sent `terminate:`,
        // on the main thread, and takes YES to commit the held quit.
        let _: () = unsafe { msg_send![ns_app, replyToApplicationShouldTerminate: true] };
    });
    if let Err(err) = scheduled {
        crate::shell_log!("the quit reply could not be scheduled on the main thread: {err}");
    }
}

/// The running NSApplication, if AppKit has one.
fn shared_application() -> Option<&'static AnyObject> {
    let class = AnyClass::get(c"NSApplication")?;
    // SAFETY: `sharedApplication` is AppKit's class method for the one
    // application of this process.
    let app: *mut AnyObject = unsafe { msg_send![class, sharedApplication] };
    // SAFETY: the application lives for the length of the process.
    Some(unsafe { app.as_ref()? })
}

#[cfg(test)]
mod tests {
    use std::ffi::CStr;
    use std::panic::{catch_unwind, AssertUnwindSafe};
    use std::ptr;
    use std::sync::atomic::{AtomicBool, Ordering};

    use objc2::rc::Retained;
    use objc2::runtime::{AnyClass, AnyObject, ClassBuilder, NSObject, Sel};
    use objc2::{msg_send, sel, ClassType};

    use super::{
        add_hook, defines_terminate, terminate_reply, HOOK, NSTERMINATE_CANCEL, NSTERMINATE_LATER,
        NSTERMINATE_NOW,
    };

    extern "C-unwind" fn count_terminate(
        _this: &AnyObject,
        _cmd: Sel,
        _sender: *mut AnyObject,
    ) -> usize {
        NSTERMINATE_NOW
    }

    /// What the stub claim of the runtime-dispatch test answers.
    ///
    /// A static, because `HOOK.set` consumes the closure that captures the
    /// claim, and the test reads the flag afterwards.
    static CLAIM_REPLIED: AtomicBool = AtomicBool::new(false);

    /// A stand-in for tao's delegate class: it answers
    /// `applicationShouldTerminate:` itself, so `add_hook` must refuse it.
    ///
    /// Rooted at `NSObject`, so no window server is needed.
    fn stub_delegate_class_with_hook(name: &CStr) -> &'static AnyClass {
        let mut builder =
            ClassBuilder::new(name, NSObject::class()).expect("the stand-in class must register");
        // SAFETY: the shape that `applicationShouldTerminate:` is called with.
        unsafe {
            builder.add_method(
                sel!(applicationShouldTerminate:),
                count_terminate as extern "C-unwind" fn(_, _, _) -> _,
            );
        }
        builder.register()
    }

    /// A stand-in class that adds nothing, so the runtime-dispatch test can
    /// install the real hook on it.
    fn stub_plain_class(name: &CStr) -> &'static AnyClass {
        ClassBuilder::new(name, NSObject::class())
            .expect("the stand-in class must register")
            .register()
    }

    // A class that answers `applicationShouldTerminate:` already -- a future
    // tao that implements the selector -- must be refused. `class_addMethod`
    // would return false there, and an unreported refusal would leave every
    // quit bypassing the cooperative shutdown with no trace.
    #[test]
    fn add_hook_reports_a_class_that_already_implements_the_selector() {
        let class = stub_delegate_class_with_hook(c"LeapMuxTerminateTaken");
        assert!(
            defines_terminate(class),
            "the stand-in must define the selector itself"
        );
        assert!(
            add_hook(class).is_err(),
            "a class that answers the selector must be refused"
        );
    }

    // The whole dispatch, end to end: the Objective-C runtime calls the hook,
    // the hook reads the claim, and the claim's answer selects AppKit's
    // terminate reply. ONE test, because `HOOK` publishes once for the process.
    #[test]
    fn add_hook_answers_through_the_runtime() {
        let class = stub_plain_class(c"LeapMuxTerminateDispatch");
        assert!(!defines_terminate(class));
        CLAIM_REPLIED.store(false, Ordering::SeqCst);
        HOOK.set(super::TerminateHook {
            claim: Box::new(|| {
                CLAIM_REPLIED.store(true, Ordering::SeqCst);
                true
            }),
        })
        .unwrap_or_else(|_| panic!("only this test publishes the hook"));
        assert!(
            add_hook(class).is_ok(),
            "the runtime must accept the hook on a class that adds nothing"
        );
        // SAFETY: the class descends from `NSObject`, which answers `new`.
        let delegate: Retained<AnyObject> = unsafe { msg_send![class, new] };
        // SAFETY: the instance carries the hook, and
        // `applicationShouldTerminate:` takes one object and answers usize.
        let reply: usize = unsafe {
            msg_send![&*delegate, applicationShouldTerminate: ptr::null_mut::<AnyObject>()]
        };

        assert!(
            CLAIM_REPLIED.load(Ordering::SeqCst),
            "the runtime dispatch must reach the claim"
        );
        assert_eq!(
            reply, NSTERMINATE_LATER,
            "a claim that succeeds must hold the quit"
        );
    }

    // A first `terminate:` claims the exit and holds the quit open.
    #[test]
    fn reply_defers_a_first_terminate() {
        assert_eq!(terminate_reply(&|| true), NSTERMINATE_LATER);
    }

    // A `terminate:` that lands while an exit another route started already
    // runs is cancelled, not deferred: the running exit ends the app, and a
    // second drain must not start.
    #[test]
    fn reply_cancels_a_terminate_during_an_exit() {
        assert_eq!(terminate_reply(&|| false), NSTERMINATE_CANCEL);
    }

    // A panic inside the claim must not cross the `extern "C-unwind"` boundary
    // into AppKit, and must not refuse the quit: the ordinary terminate
    // proceeds, and the sidecar finishes its drain by itself.
    #[test]
    fn reply_answers_now_after_a_panic() {
        let panicking = || -> bool {
            panic!("the quit claim failed");
        };
        let reply = catch_unwind(AssertUnwindSafe(|| terminate_reply(&panicking)))
            .expect("the panic must stop at terminate_reply, not escape it");
        assert_eq!(reply, NSTERMINATE_NOW);
    }
}
