package terminal

// --rc selects only this bootstrap. The bootstrap calls Xonsh's own loader
// with the configured rc files and directories in the interactive context.
const xonshInitialStartup = `
def __leapmux_install_startup():
    import os
    import time
    from pathlib import Path
    from xonsh.built_ins import XSH
    from xonsh.environ import xonshrc_context
    from xonsh.events import events

    XSH.ctx.pop("__leapmux_install_startup", None)
    prefix = "LEAPMUX_INITIAL_COMMAND_"
    launch = {key: os.environ[prefix + key] for key in ("FILE", "READY", "ACK", "NONCE", "PID")}

    # Run every pre-cmdloop hook before the command. Xonsh 0.14.4 stores the
    # handlers in a set, so registration order cannot enforce this.
    pre_cmdloop = events.on_pre_cmdloop
    fire_pre_cmdloop = pre_cmdloop.fire

    def execute_startup_command(**kwargs):
        pre_cmdloop.fire = fire_pre_cmdloop
        fire_pre_cmdloop(**kwargs)
        try:
            pid = str(os.getpid())
            if pid != launch["PID"]:
                return
            ready = Path(launch["READY"])
            command = Path(launch["FILE"])
            marker = f"{pid} {launch['NONCE']}\n"
            with ready.open("x", encoding="ascii") as stream:
                stream.write(marker)
            deadline = time.monotonic() + 30
            while not command.is_file():
                if not ready.is_file() or time.monotonic() >= deadline:
                    return
                time.sleep(0.02)
            payload = command.read_bytes().decode("utf-8")
            with Path(launch["ACK"]).open("x", encoding="ascii") as stream:
                stream.write(marker)
        finally:
            for key in launch:
                os.environ.pop(prefix + key, None)
                XSH.env.pop(prefix + key, None)
            launch.clear()
        XSH.execer.exec(payload, mode="exec", glbs=XSH.ctx, locs=XSH.ctx, filename="<LeapMux startup command>")

    pre_cmdloop.fire = execute_startup_command

    # Cache the user's files as usual. Only the private, per-launch file
    # needs --no-script-cache because that file is removed after handoff.
    XSH.execer.scriptcache = True
    loaded = xonshrc_context(
        rcfiles=XSH.env.get("XONSHRC"),
        rcdirs=XSH.env.get("XONSHRC_DIR"),
        execer=XSH.execer,
        ctx=XSH.ctx,
        env=XSH.env,
        login=True,
    )

    # The outer loader reports this bootstrap. Restore the real list before
    # the profiles' on_post_rc handlers observe it.
    post_rc = events.on_post_rc
    fire_post_rc = post_rc.fire

    def restore_loaded_profiles(**kwargs):
        post_rc.fire = fire_post_rc
        XSH.rc_files = loaded
        return fire_post_rc(**kwargs)

    post_rc.fire = restore_loaded_profiles

__leapmux_install_startup()
`
