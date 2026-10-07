#!/bin/sh
set -eu
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
mkdir "$root/source" "$root/security"
printf '{"credentials":[]}' > "$root/source/auth.json"
printf '{"grants":[]}' > "$root/source/grants.json"
export SECURITY_ROOT="$root/security" SECURITY_SOURCE="$root/source"
sh scripts/provision-mcp-security.sh
test "$(stat -c '%u:%a:%h' "$root/security/auth.json")" = '0:600:1'
before=$(sha256sum "$root/security/"*.json)
sh scripts/provision-mcp-security.sh
test "$before" = "$(sha256sum "$root/security/"*.json)"
chmod 777 "$root/security/auth.json"
if sh scripts/provision-mcp-security.sh 2>/dev/null; then exit 1; fi
chmod 600 "$root/security/auth.json"
printf changed > "$root/source/auth.json"
if sh scripts/provision-mcp-security.sh 2>/dev/null; then exit 1; fi
test "$before" = "$(sha256sum "$root/security/"*.json)"
unset SECURITY_SOURCE
sh scripts/provision-mcp-security.sh
ln "$root/security/auth.json" "$root/auth-link"
if sh scripts/provision-mcp-security.sh 2>/dev/null; then exit 1; fi
rm "$root/auth-link"
mkdir "$root/empty"
export SECURITY_ROOT="$root/empty"
if sh scripts/provision-mcp-security.sh 2>/dev/null; then exit 1; fi
ln -s "$root/missing" "$root/empty/auth.json"
export SECURITY_SOURCE="$root/source"
if sh scripts/provision-mcp-security.sh 2>/dev/null; then exit 1; fi
echo 'MCP7 security idempotence and fail-closed PASS'
