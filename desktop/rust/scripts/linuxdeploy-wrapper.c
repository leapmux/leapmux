/*
 * ELF wrapper for linuxdeploy that runs the extracted (patched) copy.
 *
 * Tauri writes to bytes 8-10 of the cached linuxdeploy binary (ELF header
 * padding), which corrupts shell script shebangs. This compiled wrapper
 * is a proper ELF binary, so those writes are harmless.
 *
 * Build:  gcc -static -O2 -o linuxdeploy-wrapper linuxdeploy-wrapper.c
 */
#include <libgen.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

int main(int argc, char **argv) {
    /* Resolve the directory containing this binary. */
    char self[4096];
    ssize_t n = readlink("/proc/self/exe", self, sizeof(self) - 1);
    if (n < 0) {
        perror("readlink /proc/self/exe");
        return 1;
    }
    self[n] = '\0';
    char *dir = dirname(self);

    /* Path to the extracted AppRun. */
    char apprun[4096];
    snprintf(apprun, sizeof(apprun), "%s/linuxdeploy-extracted/AppRun", dir);

    /* Prepend cache dir to PATH so linuxdeploy discovers its plugins. */
    const char *old_path = getenv("PATH");
    char new_path[8192];
    snprintf(new_path, sizeof(new_path), "%s:%s", dir, old_path ? old_path : "");
    setenv("PATH", new_path, 1);

    /*
     * Build new argv, filtering out --appimage-extract-and-run which is
     * an AppImage-runtime flag that the raw binary does not understand.
     */
    char **new_argv = calloc(argc + 1, sizeof(char *));
    if (!new_argv) {
        perror("calloc");
        return 1;
    }
    int j = 0;
    new_argv[j++] = apprun;
    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--appimage-extract-and-run") != 0)
            new_argv[j++] = argv[i];
    }
    new_argv[j] = NULL;

    execv(apprun, new_argv);
    perror("execv");
    return 1;
}
