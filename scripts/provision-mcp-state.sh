#!/bin/sh
# Runs only in a Linux helper container. Never print security material.
set -eu
if test ! -e /state/state; then mkdir -m 700 /state/state; fi
test -d /state/state
test ! -L /state/state
test "$(stat -c %u:%a /state/state)" = 0:700
