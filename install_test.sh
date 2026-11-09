#!/bin/sh
# Tests for install.sh's uninstall mode (FORGERUN_UNINSTALL=1). Every scenario
# runs in a fresh fake HOME with stub systemctl/launchctl/uname commands on
# PATH, so nothing real is stopped or deleted and no systemd, Docker or macOS
# machine is needed. The stubs can be told to fail or answer ambiguously, so
# the failure and fail-closed paths are tested too. Run from the repo root:
# sh install_test.sh
set -eu

cd "$(dirname "$0")"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok - %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL - %s: %s\n' "$1" "$2" >&2; }

# scenario NAME makes a sandbox: $H (fake home), $B (stub bin), $O (output),
# $CALLS (the commands the stubs were asked to run).
#
# Stub controls (environment):
#   SYSTEMD_SESSION=no        systemctl --user has no user manager to talk to
#   SYSTEMCTL_FAIL_STOP=yes   disable --now exits 1
#   SYSTEMCTL_STATE=STATE     is-active prints STATE and exits 0 only for
#                             "active" (empty string simulates an error)
#   LAUNCHCTL_FAIL_BOOTOUT=yes     bootout exits 1
#   LAUNCHCTL_STILL_LOADED=yes     print exits 0 (service is loaded)
#   LAUNCHCTL_PRINT_ERROR=yes      print exits 1 with an unrelated error
scenario() {
	S=$tmp/$1
	H=$S/home
	B=$S/bin
	O=$S/out
	CALLS=$S/calls
	mkdir -p "$H" "$B"
	: >"$CALLS"
	cat >"$B/systemctl" <<EOF
#!/bin/sh
printf 'systemctl %s\n' "\$*" >>"$CALLS"
case "\$*" in
"--user show-environment") [ "\${SYSTEMD_SESSION:-yes}" = yes ] || exit 1 ;;
*"disable --now"*) [ "\${SYSTEMCTL_FAIL_STOP:-no}" = no ] || exit 1 ;;
"--user is-active "*)
	state=\${SYSTEMCTL_STATE-inactive}
	if [ -n "\$state" ]; then printf '%s\n' "\$state"; fi
	[ "\$state" = active ] || exit 1
	exit 0 ;;
esac
exit 0
EOF
	cat >"$B/launchctl" <<EOF
#!/bin/sh
printf 'launchctl %s\n' "\$*" >>"$CALLS"
case "\$1" in
bootout) [ "\${LAUNCHCTL_FAIL_BOOTOUT:-no}" = no ] || exit 1 ;;
print)
	if [ "\${LAUNCHCTL_PRINT_ERROR:-no}" = yes ]; then
		echo "launchctl: internal error" >&2
		exit 1
	fi
	if [ "\${LAUNCHCTL_STILL_LOADED:-no}" = yes ]; then
		echo "pid = 1234"
		exit 0
	fi
	echo "Could not find service \"\$2\" in domain" >&2
	exit 1 ;;
esac
exit 0
EOF
	cat >"$B/uname" <<EOF
#!/bin/sh
case "\$1" in
-s) echo "\${STUB_OS:-Linux}" ;;
-m) echo "\${STUB_ARCH:-x86_64}" ;;
esac
exit 0
EOF
	chmod +x "$B/systemctl" "$B/launchctl" "$B/uname"
}

# run_uninstall HOME_VAL VAR=val ... runs the uninstall mode in the sandbox
# with a scrubbed environment; the exit status lands in $status.
run_uninstall() {
	hv=$1
	shift
	status=0
	env -i PATH="$B:/usr/bin:/bin" HOME="$hv" FORGERUN_UNINSTALL=1 "$@" sh ./install.sh >"$O" 2>&1 || status=$?
}

expect_file() { if [ -e "$1" ]; then ok "$2"; else bad "$2" "missing: $1"; fi; }
expect_gone() { if [ ! -e "$1" ]; then ok "$2"; else bad "$2" "still there: $1"; fi; }
expect_call() { if grep -qF "$1" "$CALLS"; then ok "$2"; else bad "$2" "no call: $1"; fi; }
expect_no_call() { if ! grep -qF "$1" "$CALLS"; then ok "$2"; else bad "$2" "unexpected call: $1"; fi; }
expect_out() { if grep -qF "$1" "$O"; then ok "$2"; else bad "$2" "output lacks: $1"; fi; }
expect_status() { if [ "$status" -eq "$1" ]; then ok "$2"; else bad "$2" "exit $status, want $1"; fi; }
expect_fail() { if [ "$status" -ne 0 ]; then ok "$1"; else bad "$1" "exit 0, want failure"; fi; }

uid=$(id -u)

# A Linux worker: its service stops and its unit goes; saying yes deletes the
# settings file and the directory; Go, Node, microsandbox's data and
# unrelated files stay.
scenario linux-worker-delete-all
mkdir -p "$H/.config/systemd/user" "$H/forgerun" "$H/.local/go/bin" "$H/.local/node/bin" "$H/.microsandbox/db" "$H/projects"
: >"$H/.config/systemd/user/forgerun-worker.service"
: >"$H/.config/systemd/user/other-app.service"
: >"$H/.forgerun-worker.env"
: >"$H/forgerun/forgerun"
: >"$H/.microsandbox/db/msb.db"
: >"$H/projects/keepme"
run_uninstall "$H" FORGERUN_DELETE_SETTINGS=yes FORGERUN_DELETE_DIR=yes
expect_status 0 "worker: uninstall succeeds"
expect_call "systemctl --user disable --now forgerun-worker" "worker: service stopped and disabled"
expect_call "systemctl --user daemon-reload" "worker: systemd reloaded"
expect_gone "$H/.config/systemd/user/forgerun-worker.service" "worker: unit file removed"
expect_file "$H/.config/systemd/user/other-app.service" "worker: unrelated service left alone"
expect_gone "$H/.forgerun-worker.env" "worker: settings deleted when told yes"
expect_gone "$H/forgerun" "worker: directory deleted when told yes"
expect_file "$H/.local/go/bin" "worker: Go toolchain left alone"
expect_file "$H/.local/node/bin" "worker: Node toolchain left alone"
expect_file "$H/.microsandbox/db/msb.db" "worker: microsandbox's data left alone"
expect_file "$H/projects/keepme" "worker: unrelated files left alone"
expect_out "left alone" "worker: says what it left alone"

# A Linux serve whose owner says no: the service goes, the directory and the
# .env inside it stay.
scenario linux-serve-keep-data
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun-serve.service"
: >"$H/forgerun/.env"
run_uninstall "$H" FORGERUN_DELETE_DIR=no
expect_status 0 "serve-no: uninstall succeeds"
expect_call "systemctl --user disable --now forgerun-serve" "serve-no: service stopped and disabled"
expect_gone "$H/.config/systemd/user/forgerun-serve.service" "serve-no: unit file removed"
expect_file "$H/forgerun/.env" "serve-no: directory and .env kept when told no"
expect_out "keeping $H/forgerun" "serve-no: says the directory was kept"

# A Linux serve told yes loses the directory, .env inside it and all.
scenario linux-serve-delete-all
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun-serve.service"
: >"$H/forgerun/.env"
run_uninstall "$H" FORGERUN_DELETE_DIR=yes
expect_status 0 "serve-yes: uninstall succeeds"
expect_gone "$H/forgerun" "serve-yes: directory deleted when told yes"
expect_out "Postgres" "serve-yes: warns that Postgres in Docker stays"

# Both roles on one machine (it was a worker, then serve): both services go.
scenario linux-both-roles
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun-worker.service"
: >"$H/.config/systemd/user/forgerun-serve.service"
: >"$H/.forgerun-worker.env"
: >"$H/forgerun/.env"
run_uninstall "$H" FORGERUN_DELETE_SETTINGS=no FORGERUN_DELETE_DIR=no
expect_status 0 "both: uninstall succeeds"
expect_call "systemctl --user disable --now forgerun-worker" "both: worker service stopped"
expect_call "systemctl --user disable --now forgerun-serve" "both: serve service stopped"
expect_gone "$H/.config/systemd/user/forgerun-worker.service" "both: worker unit removed"
expect_gone "$H/.config/systemd/user/forgerun-serve.service" "both: serve unit removed"
expect_file "$H/.forgerun-worker.env" "both: worker settings kept when told no"
expect_file "$H/forgerun/.env" "both: serve directory kept when told no"

# Nothing was ever installed as a service: say so, still succeed.
scenario linux-no-service
mkdir -p "$H/forgerun"
run_uninstall "$H" FORGERUN_DELETE_DIR=no
expect_status 0 "none: uninstall succeeds"
expect_out "no background service found" "none: reports there was no service"
expect_no_call "disable" "none: no systemctl stop attempted"
expect_file "$H/forgerun" "none: directory kept"

# Without a systemd user session the stop can't be verified: fail closed.
# The unit file and all data stay, even when told to delete.
scenario linux-no-session
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun-worker.service"
: >"$H/.forgerun-worker.env"
run_uninstall "$H" SYSTEMD_SESSION=no FORGERUN_DELETE_SETTINGS=yes FORGERUN_DELETE_DIR=yes
expect_fail "no-session: fails closed"
expect_out "can't check whether forgerun-worker is running" "no-session: explains the problem"
expect_out "no data was deleted" "no-session: says no data was deleted"
expect_file "$H/.config/systemd/user/forgerun-worker.service" "no-session: unit file kept"
expect_file "$H/.forgerun-worker.env" "no-session: settings kept despite yes"
expect_file "$H/forgerun" "no-session: directory kept despite yes"

# A failed stop with the service still active: fail closed, delete nothing.
scenario linux-stop-fails-active
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun-worker.service"
: >"$H/.forgerun-worker.env"
run_uninstall "$H" SYSTEMCTL_FAIL_STOP=yes SYSTEMCTL_STATE=active FORGERUN_DELETE_SETTINGS=yes FORGERUN_DELETE_DIR=yes
expect_fail "stop-fails-active: fails closed"
expect_out "couldn't stop forgerun-worker" "stop-fails-active: names the service"
expect_out "no data was deleted" "stop-fails-active: says no data was deleted"
expect_file "$H/.config/systemd/user/forgerun-worker.service" "stop-fails-active: unit file kept"
expect_file "$H/.forgerun-worker.env" "stop-fails-active: settings kept despite yes"
expect_file "$H/forgerun" "stop-fails-active: directory kept despite yes"

# A failed stop with a known-inactive service: safe to finish.
scenario linux-stop-fails-inactive
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun-worker.service"
run_uninstall "$H" SYSTEMCTL_FAIL_STOP=yes SYSTEMCTL_STATE=inactive FORGERUN_DELETE_DIR=yes
expect_status 0 "stop-fails-inactive: uninstall succeeds"
expect_call "systemctl --user is-active forgerun-worker" "stop-fails-inactive: activity checked after failure"
expect_gone "$H/.config/systemd/user/forgerun-worker.service" "stop-fails-inactive: unit file removed"
expect_gone "$H/forgerun" "stop-fails-inactive: directory deleted when told yes"

# A failed stop with a failed-state service is not known-inactive: fail closed.
scenario linux-stop-fails-failed-state
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun-worker.service"
run_uninstall "$H" SYSTEMCTL_FAIL_STOP=yes SYSTEMCTL_STATE=failed FORGERUN_DELETE_DIR=yes
expect_fail "stop-fails-failed-state: fails closed"
expect_out "state: failed" "stop-fails-failed-state: reports the state"
expect_file "$H/.config/systemd/user/forgerun-worker.service" "stop-fails-failed-state: unit file kept"
expect_file "$H/forgerun" "stop-fails-failed-state: directory kept despite yes"

# A failed stop with an unknown state: fail closed.
scenario linux-stop-fails-unknown
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun-worker.service"
run_uninstall "$H" SYSTEMCTL_FAIL_STOP=yes SYSTEMCTL_STATE=unknown FORGERUN_DELETE_DIR=yes
expect_fail "stop-fails-unknown: fails closed"
expect_file "$H/.config/systemd/user/forgerun-worker.service" "stop-fails-unknown: unit file kept"
expect_file "$H/forgerun" "stop-fails-unknown: directory kept despite yes"

# A failed stop where is-active itself errors (no answer): fail closed.
scenario linux-stop-fails-error
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun-worker.service"
run_uninstall "$H" SYSTEMCTL_FAIL_STOP=yes SYSTEMCTL_STATE= FORGERUN_DELETE_DIR=yes
expect_fail "stop-fails-error: fails closed"
expect_file "$H/.config/systemd/user/forgerun-worker.service" "stop-fails-error: unit file kept"
expect_file "$H/forgerun" "stop-fails-error: directory kept despite yes"

# A Mac worker: launchctl bootout with the user's gui domain, plist and its
# log removed, no systemctl involved, unrelated plist left alone.
scenario darwin-worker
mkdir -p "$H/Library/LaunchAgents" "$H/Library/Logs" "$H/forgerun"
: >"$H/Library/LaunchAgents/dev.forgerun.worker.plist"
: >"$H/Library/LaunchAgents/com.example.other.plist"
: >"$H/Library/Logs/forgerun-worker.log"
: >"$H/.forgerun-worker.env"
run_uninstall "$H" STUB_OS=Darwin STUB_ARCH=arm64 FORGERUN_DELETE_SETTINGS=yes FORGERUN_DELETE_DIR=no
expect_status 0 "mac: uninstall succeeds"
expect_call "launchctl bootout gui/$uid/dev.forgerun.worker" "mac: launchd service booted out"
expect_gone "$H/Library/LaunchAgents/dev.forgerun.worker.plist" "mac: plist removed"
expect_gone "$H/Library/Logs/forgerun-worker.log" "mac: service log removed"
expect_file "$H/Library/LaunchAgents/com.example.other.plist" "mac: unrelated plist left alone"
expect_no_call "systemctl --user disable" "mac: no systemd on a Mac"
expect_gone "$H/.forgerun-worker.env" "mac: settings deleted when told yes"
expect_file "$H/forgerun" "mac: directory kept when told no"

# A failed bootout with the service still loaded: fail closed, delete nothing.
scenario darwin-bootout-fails
mkdir -p "$H/Library/LaunchAgents" "$H/Library/Logs" "$H/forgerun"
: >"$H/Library/LaunchAgents/dev.forgerun.worker.plist"
: >"$H/.forgerun-worker.env"
run_uninstall "$H" STUB_OS=Darwin STUB_ARCH=arm64 LAUNCHCTL_FAIL_BOOTOUT=yes LAUNCHCTL_STILL_LOADED=yes FORGERUN_DELETE_SETTINGS=yes FORGERUN_DELETE_DIR=yes
expect_fail "mac-bootout-fails: fails closed"
expect_call "launchctl print gui/$uid/dev.forgerun.worker" "mac-bootout-fails: load state checked after failure"
expect_out "may still be running" "mac-bootout-fails: explains the problem"
expect_file "$H/Library/LaunchAgents/dev.forgerun.worker.plist" "mac-bootout-fails: plist kept"
expect_file "$H/.forgerun-worker.env" "mac-bootout-fails: settings kept despite yes"
expect_file "$H/forgerun" "mac-bootout-fails: directory kept despite yes"

# A failed bootout with a known not-found print answer: safe to finish.
scenario darwin-bootout-not-loaded
mkdir -p "$H/Library/LaunchAgents" "$H/forgerun"
: >"$H/Library/LaunchAgents/dev.forgerun.worker.plist"
run_uninstall "$H" STUB_OS=Darwin STUB_ARCH=arm64 LAUNCHCTL_FAIL_BOOTOUT=yes LAUNCHCTL_STILL_LOADED=no FORGERUN_DELETE_DIR=no
expect_status 0 "mac-not-loaded: uninstall succeeds"
expect_gone "$H/Library/LaunchAgents/dev.forgerun.worker.plist" "mac-not-loaded: plist removed"

# A failed bootout where print itself errors (no answer): fail closed.
scenario darwin-print-error
mkdir -p "$H/Library/LaunchAgents" "$H/forgerun"
: >"$H/Library/LaunchAgents/dev.forgerun.worker.plist"
: >"$H/.forgerun-worker.env"
run_uninstall "$H" STUB_OS=Darwin STUB_ARCH=arm64 LAUNCHCTL_FAIL_BOOTOUT=yes LAUNCHCTL_PRINT_ERROR=yes FORGERUN_DELETE_SETTINGS=yes FORGERUN_DELETE_DIR=yes
expect_fail "mac-print-error: fails closed"
expect_file "$H/Library/LaunchAgents/dev.forgerun.worker.plist" "mac-print-error: plist kept"
expect_file "$H/.forgerun-worker.env" "mac-print-error: settings kept despite yes"
expect_file "$H/forgerun" "mac-print-error: directory kept despite yes"

# FORGERUN_DIR pointing at the home directory itself must never be deleted,
# however it is written: directly, with a trailing slash, with HOME itself
# carrying a trailing slash, through a resolving path, or as /.
scenario dir-guard-home
mkdir -p "$H/forgerun"
run_uninstall "$H" FORGERUN_DIR="$H" FORGERUN_DELETE_DIR=yes
expect_fail "guard-home: refuses to delete home"
expect_file "$H" "guard-home: home directory intact"
expect_out "not deleting" "guard-home: explains the refusal"

scenario dir-guard-home-trailing
mkdir -p "$H/keep"
run_uninstall "$H/" FORGERUN_DIR="$H/" FORGERUN_DELETE_DIR=yes
expect_fail "guard-home-trailing: refuses HOME with a trailing slash"
expect_file "$H/keep" "guard-home-trailing: home contents intact"
expect_out "not deleting" "guard-home-trailing: explains the refusal"

scenario dir-guard-dir-trailing
mkdir -p "$H/keep"
run_uninstall "$H" FORGERUN_DIR="$H/" FORGERUN_DELETE_DIR=yes
expect_fail "guard-dir-trailing: refuses FORGERUN_DIR with a trailing slash"
expect_file "$H/keep" "guard-dir-trailing: home contents intact"

scenario dir-guard-resolves-home
mkdir -p "$H/sub"
run_uninstall "$H" FORGERUN_DIR="$H/sub/.." FORGERUN_DELETE_DIR=yes
expect_fail "guard-resolves: refuses a path resolving to home"
expect_file "$H/sub" "guard-resolves: home directory intact"
expect_out "resolves to" "guard-resolves: explains the refusal"

scenario dir-guard-root
run_uninstall / FORGERUN_DIR=/ FORGERUN_DELETE_DIR=yes
expect_fail "guard-root: refuses to delete /"
expect_out "not deleting" "guard-root: explains the refusal"

# A symlinked FORGERUN_DIR is never followed: the link and its target stay.
scenario dir-symlink
mkdir -p "$H/realdir"
: >"$H/realdir/keepme"
ln -s "$H/realdir" "$H/link"
run_uninstall "$H" FORGERUN_DIR="$H/link" FORGERUN_DELETE_DIR=yes
expect_status 0 "symlink: uninstall otherwise succeeds"
expect_out "it's a symlink" "symlink: explains the refusal"
expect_file "$H/link" "symlink: link kept"
expect_file "$H/realdir/keepme" "symlink: target kept"

# Unit files whose names merely start with forgerun are not ours to touch.
scenario unrelated-units
mkdir -p "$H/.config/systemd/user" "$H/forgerun"
: >"$H/.config/systemd/user/forgerun.service"
: >"$H/.config/systemd/user/forgerun-worker-extra.service"
run_uninstall "$H" FORGERUN_DELETE_DIR=no
expect_status 0 "unrelated: uninstall succeeds"
expect_no_call "disable --now forgerun.service" "unrelated: forgerun.service not stopped"
expect_no_call "disable --now forgerun-worker-extra" "unrelated: forgerun-worker-extra not stopped"
expect_file "$H/.config/systemd/user/forgerun.service" "unrelated: forgerun.service kept"
expect_file "$H/.config/systemd/user/forgerun-worker-extra.service" "unrelated: forgerun-worker-extra.service kept"
expect_out "no background service found" "unrelated: reports no forgerun service"

printf '%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
