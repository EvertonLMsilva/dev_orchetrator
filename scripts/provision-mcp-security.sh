#!/bin/sh
# Runs only in a Linux helper container. Never print security material.
set -eu
umask 077
root=${SECURITY_ROOT:-/security}
source=${SECURITY_SOURCE:-}
fail() { echo 'Security volume validation/provisioning failed' >&2; exit 1; }
test -d "$root" && test ! -L "$root" || fail
test ! -L "$root/auth.json" && test ! -L "$root/grants.json" || fail
check() {
    test -f "$1" && test ! -L "$1" || fail
    test "$(stat -c '%u:%g:%a:%h' "$1")" = '0:0:600:1' || fail
    test "$(stat -c '%s' "$1")" -le 65536 && test -s "$1" || fail
}
if test -e "$root/auth.json" || test -e "$root/grants.json"; then
    check "$root/auth.json"
    check "$root/grants.json"
    if test -n "$source"; then
        cmp -s "$source/auth.json" "$root/auth.json" || fail
        cmp -s "$source/grants.json" "$root/grants.json" || fail
    fi
else
    test -n "$source" || fail
    for name in auth.json grants.json; do
        test -f "$source/$name" && test ! -L "$source/$name" || fail
        test -s "$source/$name" && test "$(stat -c '%s' "$source/$name")" -le 65536 || fail
    done
    # Exclusive staging directory prevents simultaneous provisioning.
    mkdir "$root/.provisioning" || fail
    for name in auth.json grants.json; do
        install -m 600 -o 0 -g 0 "$source/$name" "$root/.provisioning/$name"
        check "$root/.provisioning/$name"
    done
    mv "$root/.provisioning/auth.json" "$root/auth.json"
    mv "$root/.provisioning/grants.json" "$root/grants.json"
    rmdir "$root/.provisioning"
fi
