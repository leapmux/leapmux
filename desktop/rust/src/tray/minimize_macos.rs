//! macOS: turn a minimize REQUEST into a hide, before AppKit performs it.
//!
//! Every affordance that LeapMux offers reaches `-[NSWindow miniaturize:]`. The
//! yellow traffic light's action IS that selector, with the window as its
//! target, for a plain click and for an Option-click alike.
//! `performMiniaturize:` -- Cmd+M, the Window menu's Minimize item and a
//! title-bar double click -- calls it in turn. And tauri's own
//! `Window::minimize` calls it directly. So one override covers them all. An
//! override on a single affordance would leave the others disobeying the
//! preference.
//!
//! `-[NSApplication miniaturizeAll:]` is the measured exception. It
//! miniaturizes the window without the selector, so the override does not see
//! it. LeapMux builds no menu item that sends it, and the yellow button does
//! not send it either. So no affordance of this application reaches that path.
//!
//! An override, and not an `NSWindowDidMiniaturizeNotification` observer,
//! because nothing can undo a miniaturize once AppKit performs it. There is no
//! `windowShouldMiniaturize:`, and the notification arrives when the window is
//! already miniaturized. Neither way out of that state works:
//!
//!   * `deminiaturize:` puts the window back ON SCREEN, every time. It is
//!     asynchronous, so it does nothing in the turn that calls it. It finishes
//!     after the `orderOut:` on the next line, and restores the window that the
//!     shell just hid. An `orderOut:` first does not help either: it re-shows a
//!     window that is already off the screen list.
//!   * `orderOut:` alone leaves the window miniaturized, and the Dock tile
//!     follows `isMiniaturized` rather than the screen list. The window
//!     disappears and its tile stays in the Dock, which is the state that the
//!     preference exists to prevent.
//!
//! So the window must never enter the miniaturized state at all. The override
//! hides the window and skips `super`, which leaves it off screen and NOT
//! miniaturized: no Dock tile, and no minimize animation to undo.
//!
//! `install` adds the method to the window's OWN class -- tao's `TaoWindow`,
//! which tao registers once for the whole process -- and the override compares
//! the receiver against the main window, so the policy reaches that window
//! alone. Two other places to put it look simpler and are not.
//!
//! A swizzle of `-[NSWindow miniaturize:]` diverts every window in the process,
//! including the panels that AppKit opens, and needs the same receiver check
//! anyway.
//!
//! A runtime SUBCLASS set on the instance with `object_setClass` crashes the
//! process, and this module used to do exactly that. Something inside AppKit
//! observes a key path on the main window before `tray::install` runs, so the
//! window's live class is a generated `NSKVONotifying_TaoWindow` by then, and
//! a subclass of it inherits `-_isKVOA`. Foundation reads that method to decide whether a class
//! is one of its own. When it answers yes,
//! `_NSKVONotifyingOriginalClassForIsa` takes the original class out of the
//! class's INDEXED IVARS, which only a class that `objc_allocateClassPair`
//! reserved the space for carries. A subclass built without them reads nil
//! there, `-[NSKeyValueContainerClass initWithOriginalClass:]` caches
//! `class_getMethodImplementation(nil, @selector(observationInfo))`, which is
//! NULL, and the next observer that AppKit registers on the window calls that
//! NULL: a jump to address 0 during the first layout of the window.
//!
//! A class that the window already inherits from has none of that. KVO puts its
//! generated subclass ABOVE the override, which keeps the override reachable,
//! and the restore that removes the generated subclass cannot take the override
//! with it.

use std::ffi::CStr;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::ptr::{self, NonNull};
use std::sync::{Arc, OnceLock};

use objc2::ffi::class_addMethod;
use objc2::runtime::{AnyClass, AnyObject, Imp, Sel};
use objc2::{msg_send, sel};
use tauri::WebviewWindow;

use super::TrayState;

/// The Objective-C type encoding of `-[NSWindow miniaturize:]`: no return
/// value, an object receiver, the selector, and one object argument.
///
/// Spelled out because the method goes on a class that already exists, which
/// `class_addMethod` takes an encoding for. objc2 derives one from the Rust
/// types instead, but only inside the class builder, and a builder makes a new
/// class. `the_override_matches_the_signature_of_nswindow_miniaturize`
/// compares what this registers against AppKit's own encoding.
const MINIATURIZE_TYPES: &CStr = c"v@:@";

/// What the override needs to answer a minimize.
///
/// A process global, because what it describes is one. `install` returns
/// before it publishes a second hook, so the fields always describe the one
/// window that it hooked.
struct MinimizeHook {
    /// Where a `super` send starts: the class ABOVE the one that carries the
    /// override, which holds the ordinary minimize.
    ///
    /// Recorded here, never read off the receiver. A KVO observer on the
    /// window makes the receiver's live class a subclass of the one that
    /// carries the override, and a `super` relative to the receiver's own
    /// class sends `miniaturize:` straight back into this module.
    super_class: &'static AnyClass,
    /// The address of the window that the policy applies to.
    ///
    /// Every tao window in the process shares the class that carries the
    /// override, so the override must recognise its own window. An address
    /// identifies it: tauri owns the main window for the length of the
    /// process, and `answer` holds a `WebviewWindow` for it besides.
    window: usize,
    /// Answers one minimize on the window that `install` hooked. Reports
    /// whether it hid that window.
    ///
    /// A closure over the `WebviewWindow` and the `TrayState` that `install`
    /// received, so the override acts on the window it was given and looks
    /// none up by label. It is also the seam that the tests drive: a
    /// `WebviewWindow` needs a running application, and a stub answer does not.
    answer: Box<dyn Fn() -> bool + Send + Sync>,
}

static HOOK: OnceLock<MinimizeHook> = OnceLock::new();

/// Install the minimize override on the main window.
///
/// Each branch below writes a message before it returns. Without the override
/// a minimize keeps its ordinary behaviour. That looks like "LeapMux ignores my
/// setting", and a silent return leaves the user nothing to report it with.
///
/// Standard error, and not the `BehaviorRefusal` channel that a failed tray
/// icon and a refused login item use. That channel exists for a failure the
/// MACHINE causes, which varies per user and needs the message on the
/// Preferences row. Every branch here is impossible by construction instead:
/// `tauri.conf.json` declares one window, `tray::install` calls this once for
/// the process, and tao's window class answers no `miniaturize:` of its own.
/// And the fallback strands nobody, because the ordinary minimize leaves a
/// window in the Dock.
pub(crate) fn install(window: &WebviewWindow, state: Arc<TrayState>) {
    let Ok(ns_window) = window.ns_window() else {
        crate::shell_log!("the main window has no NSWindow; a minimize cannot hide it");
        return;
    };
    let Some(object) = NonNull::new(ns_window.cast::<AnyObject>()) else {
        crate::shell_log!("the main window's NSWindow is null; a minimize cannot hide it");
        return;
    };
    // SAFETY: `ns_window` is the live NSWindow that tauri owns and keeps for
    // the length of the process, and `install` runs on the main thread, where
    // every call below belongs.
    let object = unsafe { object.as_ref() };
    let Some(target) = window_class(object) else {
        return;
    };
    if defines_miniaturize(target) {
        // Either `install` ran twice, or another party overrode the method on
        // the same class. Either way this window keeps the ordinary minimize.
        crate::shell_log!(
            "{} answers miniaturize: already; a minimize cannot hide the window",
            target.name().to_string_lossy()
        );
        return;
    }
    let Some(super_class) = target.superclass() else {
        // Impossible: `target` inherits `miniaturize:` from AppKit, so it has
        // a superclass. A root class would leave the override with nothing to
        // defer to.
        crate::shell_log!(
            "{} is a root class; a minimize cannot hide the window",
            target.name().to_string_lossy()
        );
        return;
    };
    // `install` publishes the hook BEFORE it adds the method, so a minimize
    // that reaches the override never finds the hook missing.
    let hooked = window.clone();
    if HOOK
        .set(MinimizeHook {
            super_class,
            window: ptr::from_ref(object) as usize,
            answer: Box::new(move || super::handle_minimize(&state, &hooked)),
        })
        .is_err()
    {
        // Impossible: a second install finds the override on the class and
        // returns above. The branch stays, because `OnceLock::set` reports a
        // `Result` that nothing else can rule out. It returns before the method
        // goes on the class, so the window keeps the ordinary minimize.
        crate::shell_log!("a minimize override is already published; this one is dropped");
        return;
    }
    if !add_override(target) {
        // Impossible for the same reason: the runtime refuses `class_addMethod`
        // only for a method that the class defines already. The published hook
        // is inert without a method to reach it.
        crate::shell_log!(
            "the runtime refused the miniaturize: override on {}; \
             a minimize cannot hide the window",
            target.name().to_string_lossy()
        );
    }
}

/// The class to put the override on: the class the window was CREATED as.
///
/// `-class` and NOT `object_getClass`. AppKit observes key paths on the main
/// window, so the live class is a subclass that Key-Value Observing generated,
/// and the runtime removes that subclass again when the last observer goes
/// away. `-class` answers past it, which is where an override survives.
///
/// Answers `None` when the window does not inherit from the class it names.
/// `-class` is a method like any other, and an override that answers an
/// unrelated class would put `miniaturize:` on a class whose own instances
/// then run the policy for a window they are not.
fn window_class(object: &AnyObject) -> Option<&'static AnyClass> {
    // SAFETY: `-class` takes no argument and answers a class object, and every
    // registered class lives for the length of the process.
    let created: Option<&'static AnyClass> = unsafe {
        let class: *const AnyClass = msg_send![object, class];
        class.as_ref()
    };
    let Some(created) = created else {
        crate::shell_log!("the main window answered no class; a minimize cannot hide it");
        return None;
    };
    let live = object.class();
    if !inherits_from(live, created) {
        crate::shell_log!(
            "the main window is a {} and answers {}, which it does not inherit from; \
             a minimize cannot hide it",
            live.name().to_string_lossy(),
            created.name().to_string_lossy()
        );
        return None;
    }
    Some(created)
}

/// Whether `class` is `ancestor`, or descends from it.
fn inherits_from(class: &AnyClass, ancestor: &AnyClass) -> bool {
    let mut next = Some(class);
    while let Some(class) = next {
        if ptr::eq(class, ancestor) {
            return true;
        }
        next = class.superclass();
    }
    false
}

/// Whether `class` defines `miniaturize:` ITSELF.
///
/// `instance_method` is `class_getInstanceMethod`, which searches the
/// superclasses too, and every window class inherits AppKit's own
/// implementation. `instance_methods` is `class_copyMethodList`, which lists
/// the class's own methods alone.
fn defines_miniaturize(class: &AnyClass) -> bool {
    class
        .instance_methods()
        .iter()
        .any(|method| method.name() == sel!(miniaturize:))
}

/// Add the `miniaturize:` override to `class`. Reports whether the runtime
/// accepted it, which it refuses for a class that defines the method already.
fn add_override(class: &AnyClass) -> bool {
    // SAFETY: `class` is a registered class, and `MINIATURIZE_TYPES` states
    // the arguments and the return value that `miniaturize` takes.
    unsafe {
        class_addMethod(
            ptr::from_ref(class).cast_mut(),
            sel!(miniaturize:),
            miniaturize_imp(),
            MINIATURIZE_TYPES.as_ptr(),
        )
    }
    .as_bool()
}

/// The override, as the untyped function pointer that `class_addMethod` takes.
///
/// objc2 builds one from a typed function inside its class builder, and that
/// path is closed here: a builder makes a new class, and this method goes on a
/// class that exists.
fn miniaturize_imp() -> Imp {
    // SAFETY: `Imp` is the same `extern "C-unwind"` function pointer with its
    // signature erased. The runtime calls it with the arguments that
    // `MINIATURIZE_TYPES` declares, and those are the ones `miniaturize` takes.
    unsafe {
        std::mem::transmute::<extern "C-unwind" fn(&AnyObject, Sel, *mut AnyObject), Imp>(
            miniaturize,
        )
    }
}

/// `-[NSWindow miniaturize:]` on tao's window class.
///
/// `super` runs for every answer except "hide". The receiver is another
/// window, the preference keeps the window in the Dock, the tray failed to
/// build, or the answer itself failed. Each one means the ordinary minimize,
/// which is the safe direction, because the user keeps a window they can reach.
///
/// The answer runs inside `catch_unwind`, because a panic that unwinds into the
/// AppKit frame that sent `miniaturize:` skips every Objective-C cleanup on the
/// way out, and reports a stack with no LeapMux frame in it. tao installs the
/// same barrier at each of its own callbacks (`stop_app_on_panic`). A panic
/// reads as "not hidden", so the ordinary minimize proceeds.
extern "C-unwind" fn miniaturize(this: &AnyObject, _cmd: Sel, sender: *mut AnyObject) {
    let Some(hook) = HOOK.get() else {
        // Impossible: `install` publishes the hook before it adds the method,
        // and a `OnceLock` never empties. Without one there is no class to
        // send `super` to, so this window does not minimize at all.
        crate::shell_log!("a minimize arrived before the override was published; it does nothing");
        return;
    };
    // Every tao window in the process shares the class that carries this
    // override. The main window is the one with a policy, so the rest fall
    // through to the ordinary minimize.
    if ptr::from_ref(this) as usize == hook.window {
        let hidden = catch_unwind(AssertUnwindSafe(|| (hook.answer)())).unwrap_or_else(|_| {
            crate::shell_log!("the minimize policy panicked; the window minimizes as usual");
            false
        });
        if hidden {
            return;
        }
    }
    // SAFETY: the lookup starts at `super_class`, which is above the class
    // that carries this method, so it finds the ordinary minimize and not this
    // function again. `miniaturize:` takes one object and returns nothing.
    let _: () = unsafe { msg_send![super(this, hook.super_class), miniaturize: sender] };
}

#[cfg(test)]
mod tests {
    use std::ffi::{c_void, CStr};
    use std::ptr;
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

    use objc2::rc::Retained;
    use objc2::runtime::{AnyClass, AnyObject, ClassBuilder, Method, NSObject, Sel};
    use objc2::{msg_send, sel, ClassType};

    use super::{add_override, defines_miniaturize, window_class, MinimizeHook, HOOK};

    /// The prefix that Key-Value Observing gives to the class it generates.
    const KVO_CLASS_PREFIX: &[u8] = b"NSKVONotifying_";

    /// How many times a stand-in superclass answered `miniaturize:`.
    static SUPER_CALLS: AtomicUsize = AtomicUsize::new(0);
    /// What the stub hook answers for the next `miniaturize:`.
    static HOOK_HIDES: AtomicBool = AtomicBool::new(false);
    /// Whether the stub hook panics instead of answering.
    static HOOK_PANICS: AtomicBool = AtomicBool::new(false);

    extern "C-unwind" fn count_miniaturize(_this: &AnyObject, _cmd: Sel, _sender: *mut AnyObject) {
        SUPER_CALLS.fetch_add(1, Ordering::SeqCst);
    }

    extern "C-unwind" fn nil_value(_this: &AnyObject, _cmd: Sel) -> *mut AnyObject {
        ptr::null_mut()
    }

    /// A stand-in for AppKit's `NSWindow`: it answers `miniaturize:` and counts,
    /// and it answers the key path `foo`, which Key-Value Observing needs to
    /// observe an instance.
    ///
    /// Rooted at `NSObject` rather than `NSWindow`, so a test can create an
    /// instance of a subclass without a window server.
    fn stub_window_class(name: &CStr) -> &'static AnyClass {
        let mut builder =
            ClassBuilder::new(name, NSObject::class()).expect("the stand-in class must register");
        // SAFETY: the shapes that `miniaturize:` and a key-path getter are
        // called with.
        unsafe {
            builder.add_method(
                sel!(miniaturize:),
                count_miniaturize as extern "C-unwind" fn(_, _, _),
            );
            builder.add_method(sel!(foo), nil_value as extern "C-unwind" fn(_, _) -> _);
        }
        builder.register()
    }

    /// A stand-in for tao's `TaoWindow`: it adds nothing, so `miniaturize:`
    /// reaches it only through the override that a test installs.
    ///
    /// Each test names its own classes, because `objc_registerClassPair` cannot
    /// be undone and a name registers once for the whole process.
    fn stub_subclass(name: &CStr, superclass: &AnyClass) -> &'static AnyClass {
        ClassBuilder::new(name, superclass)
            .expect("the stand-in subclass must register")
            .register()
    }

    fn nsstring(text: &CStr) -> Retained<AnyObject> {
        let class = AnyClass::get(c"NSString").expect("Foundation must register NSString");
        // SAFETY: `stringWithUTF8String:` takes a C string and answers an
        // autoreleased NSString.
        unsafe { msg_send![class, stringWithUTF8String: text.as_ptr()] }
    }

    /// Register `observer` on the key path of `key`, the way AppKit registers
    /// its own observers on the main window.
    fn observe(object: &AnyObject, observer: &NSObject, key: &AnyObject) {
        // SAFETY: NSObject's own registration: an observer, a key path, no
        // options and no context.
        unsafe {
            let _: () = msg_send![
                object,
                addObserver: observer,
                forKeyPath: key,
                options: 0_usize,
                context: ptr::null_mut::<c_void>(),
            ];
        }
    }

    fn unobserve(object: &AnyObject, observer: &NSObject, key: &AnyObject) {
        // SAFETY: the pair of `observe`. An object that deallocates with an
        // observer still registered raises.
        unsafe {
            let _: () = msg_send![object, removeObserver: observer, forKeyPath: key];
        }
    }

    /// The `miniaturize:` that the runtime dispatches for `class`.
    ///
    /// `class_getInstanceMethod` searches the superclasses and answers the
    /// method it found there, so an inherited method compares equal to the one
    /// on the class that defines it.
    fn miniaturize_method(class: &AnyClass) -> &Method {
        class
            .instance_method(sel!(miniaturize:))
            .expect("the class must answer miniaturize:")
    }

    // Where the override lands: on the class itself, so every instance of it
    // reaches the override and no instance changes class.
    #[test]
    fn the_override_goes_on_the_window_class() {
        let base = stub_window_class(c"LeapMuxOverrideBase");
        let target = stub_subclass(c"LeapMuxOverride", base);
        assert!(
            !defines_miniaturize(target),
            "the stand-in must inherit miniaturize: and not define it"
        );

        assert!(add_override(target), "the runtime must accept the override");

        assert!(
            defines_miniaturize(target),
            "the override must be on the class itself and not inherited"
        );
        assert!(
            !add_override(target),
            "a second install on the same class must be refused"
        );
    }

    // The class that the override goes on, for a window that AppKit already
    // observes. THE regression: the live class of such a window is a class that
    // Key-Value Observing generated, and a class built under one crashes the
    // process on the next observer. See the module documentation.
    #[test]
    fn an_observed_window_takes_the_override_on_the_class_it_was_created_as() {
        let base = stub_window_class(c"LeapMuxObservedBase");
        let created = stub_subclass(c"LeapMuxObserved", base);
        // SAFETY: `created` descends from `NSObject`, which answers `new`.
        let window: Retained<AnyObject> = unsafe { msg_send![created, new] };
        let observer = NSObject::new();
        let key = nsstring(c"foo");

        observe(&window, &observer, &key);
        assert!(
            window.class().name().to_bytes().starts_with(KVO_CLASS_PREFIX),
            "the first observer must leave a generated class on the window"
        );

        assert!(
            ptr::eq(
                window_class(&window).expect("the window must answer its own class"),
                created
            ),
            "the override must go on the class the window was created as, \
             and not on the generated one"
        );
        assert!(add_override(created));

        // The call that crashed. A class built under the generated one leaves
        // Foundation a NULL `observationInfo` implementation to call here.
        observe(&window, &observer, &key);

        assert!(
            ptr::eq(
                miniaturize_method(window.class()),
                miniaturize_method(created)
            ),
            "an observed window must still dispatch miniaturize: to the override"
        );
        unobserve(&window, &observer, &key);
        unobserve(&window, &observer, &key);
    }

    // A window that answers a class it does not inherit from takes no override.
    // The method would land on a class whose own instances then run the policy
    // for a window they are not.
    #[test]
    fn a_window_that_answers_an_unrelated_class_takes_no_override() {
        extern "C-unwind" fn unrelated_class(_this: &AnyObject, _cmd: Sel) -> *const AnyClass {
            AnyClass::get(c"NSString").expect("Foundation must register NSString")
        }
        let mut builder = ClassBuilder::new(c"LeapMuxLyingWindow", NSObject::class())
            .expect("the stand-in class must register");
        // SAFETY: `-class` takes no argument and answers a class.
        unsafe {
            builder.add_method(sel!(class), unrelated_class as extern "C-unwind" fn(_, _) -> _);
        }
        let class = builder.register();
        // SAFETY: `class` descends from `NSObject`, which answers `new`.
        let window: Retained<AnyObject> = unsafe { msg_send![class, new] };

        assert!(window_class(&window).is_none());
    }

    // The Objective-C signature of the override, against AppKit's own.
    //
    // objc2 verifies this itself for a class that its builder makes, and this
    // method goes on a class that exists, so `add_override` states the encoding
    // as text instead. A wrong encoding registers a method that contradicts the
    // way AppKit calls it, and the first real minimize reads its arguments from
    // the wrong place.
    //
    // `NSWindow` resolves in any process that links AppKit, and the test
    // creates no window, so this needs no window server.
    #[test]
    fn the_override_matches_the_signature_of_nswindow_miniaturize() {
        let nswindow = AnyClass::get(c"NSWindow").expect("AppKit must register NSWindow");
        let appkit = nswindow
            .instance_method(sel!(miniaturize:))
            .expect("NSWindow must answer miniaturize:");
        let target = stub_subclass(c"LeapMuxSignatureShape", nswindow);
        assert!(add_override(target), "the runtime must accept the override");
        let ours = target
            .instance_method(sel!(miniaturize:))
            .expect("the override must be on the class");
        assert!(
            defines_miniaturize(target),
            "without its own method the comparison below reads NSWindow twice"
        );

        assert_eq!(
            ours.return_type().to_bytes(),
            appkit.return_type().to_bytes(),
            "the override must return what AppKit returns"
        );
        assert_eq!(
            ours.arguments_count(),
            appkit.arguments_count(),
            "the override must take the arguments AppKit passes"
        );
        for index in 0..appkit.arguments_count() {
            assert_eq!(
                ours.argument_type(index).map(|ty| ty.to_bytes().to_vec()),
                appkit.argument_type(index).map(|ty| ty.to_bytes().to_vec()),
                "argument {index} must carry AppKit's encoding"
            );
        }
    }

    // The whole dispatch, end to end: the Objective-C runtime calls the
    // override, the override reads the live answer, and `super` runs for every
    // answer except "hide". It pins the receiver check that leaves every other
    // window on the same class alone, and the barrier that keeps a panic out of
    // the AppKit frame that sent `miniaturize:`.
    //
    // ONE test, because `HOOK` publishes once for the process. It drives every
    // answer through the two statics that the stub `answer` reads on each call,
    // the way the real one reads `TrayState`.
    #[test]
    fn the_override_calls_super_unless_the_answer_hides_the_window() {
        let base = stub_window_class(c"LeapMuxDispatchBase");
        let installed_class = stub_subclass(c"LeapMuxDispatch", base);
        // SAFETY: `installed_class` descends from `NSObject`, which answers
        // `new`.
        let window: Retained<AnyObject> = unsafe { msg_send![installed_class, new] };
        let other: Retained<AnyObject> = unsafe { msg_send![installed_class, new] };
        HOOK.set(MinimizeHook {
            super_class: base,
            window: ptr::from_ref(&*window) as usize,
            answer: Box::new(|| {
                assert!(
                    !HOOK_PANICS.load(Ordering::SeqCst),
                    "the minimize policy failed"
                );
                HOOK_HIDES.load(Ordering::SeqCst)
            }),
        })
        .unwrap_or_else(|_| panic!("only this test publishes the hook"));
        assert!(add_override(installed_class));
        let minimize = |receiver: &AnyObject| {
            // SAFETY: the receiver carries the override, and `miniaturize:`
            // takes one object and returns nothing.
            let _: () = unsafe { msg_send![receiver, miniaturize: ptr::null_mut::<AnyObject>()] };
            SUPER_CALLS.load(Ordering::SeqCst)
        };

        // "Keep in the Dock": the ordinary minimize must proceed.
        HOOK_HIDES.store(false, Ordering::SeqCst);
        assert_eq!(
            minimize(&window),
            1,
            "super must run when the answer does not hide"
        );

        // "Hide to the menu bar": the window must never reach `super`, or it
        // enters the miniaturized state that this module exists to prevent.
        HOOK_HIDES.store(true, Ordering::SeqCst);
        assert_eq!(minimize(&window), 1, "super must not run when the answer hides");

        // Another window on the same class carries no policy, so it minimizes
        // even while the answer hides the main window.
        assert_eq!(
            minimize(&other),
            2,
            "a window that the hook does not name must reach super"
        );

        // And back, because the override reads the answer on every click. A
        // preference that the user changes mid-session must reach the next
        // minimize.
        HOOK_HIDES.store(false, Ordering::SeqCst);
        assert_eq!(minimize(&window), 3, "the answer is read on every call");

        // A panic must stop at the override. Without `catch_unwind` it unwinds
        // through the Objective-C frames that dispatched the selector, and this
        // assertion never runs.
        //
        // The panic report is left alone on purpose. `std::panic::set_hook` is
        // process-global, and `cargo test` runs this file beside ninety other
        // tests on a thread pool, so a silencing hook would swallow a real
        // failure in one of them. libtest captures the expected report instead,
        // and prints it only when THIS test fails.
        HOOK_PANICS.store(true, Ordering::SeqCst);
        let calls = minimize(&window);
        HOOK_PANICS.store(false, Ordering::SeqCst);
        assert_eq!(calls, 4, "a panic must leave the ordinary minimize alone");
    }
}
