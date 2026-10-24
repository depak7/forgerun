#!/bin/sh
# forgerun installer. It checks this machine, installs what's missing (git, a C
# compiler, Go; Node for the web UI), builds forgerun, checks the setup with
# `forgerun doctor`, and runs it in the background as a worker or as
# everything on this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/depak7/forgerun/main/install.sh | sh
#
# Run it again to update: it pulls, rebuilds and restarts, keeping your settings.
#
# Every question can be answered ahead with a variable (needed without a terminal):
#   FORGERUN_ROLE=worker|serve        what this machine is for
#   FORGERUN_SERVER, FORGERUN_WORKER_TOKEN     the orchestrator, for a worker
#   CURSOR_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, OPENCODE_API_KEY, GITHUB_TOKEN
#   FORGERUN_SERVICE=yes|no           run it in the background, now and at boot (default yes)
#   FORGERUN_DIR (~/forgerun), FORGERUN_REPO, FORGERUN_BRANCH (main)
#   FORGERUN_DOCTOR_FLAGS             extra flags for forgerun doctor, e.g. -quick
# shellcheck disable=SC2034,SC2154 # variables set and read through eval in ask()
set -eu

REPO=${FORGERUN_REPO:-https://github.com/depak7/forgerun.git}
BRANCH=${FORGERUN_BRANCH:-main}
DIR=${FORGERUN_DIR:-$HOME/forgerun}
WORKER_ENV=$HOME/.forgerun-worker.env
LOCAL=$HOME/.local

say() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() {
	printf '\033[1;31merror:\033[0m %s\n' "$*" >&2
	exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

# A terminal to ask on: with `curl | sh`, stdin is the script itself.
tty_ok() { [ -r /dev/tty ] && [ -w /dev/tty ] && (: </dev/tty) 2>/dev/null; }

# ask VAR "question" [default]: VAR from the environment, else the terminal.
ask() {
	eval "cur=\${$1:-}"
	if [ -n "$cur" ]; then return; fi
	if ! tty_ok; then
		[ -n "${3:-}" ] || die "set $1 (no terminal to ask on)"
		eval "$1=\$3"
		return
	fi
	printf '%s%s: ' "$2" "${3:+ [$3]}" >/dev/tty
	read -r ans </dev/tty || ans=
	eval "$1=\${ans:-\${3:-}}"
}

# ask_secret VAR "question": like ask, typed hidden; Enter skips it.
ask_secret() {
	eval "cur=\${$1:-}"
	if [ -n "$cur" ] || ! tty_ok; then return; fi
	printf '%s (Enter to skip): ' "$2" >/dev/tty
	stty -echo </dev/tty 2>/dev/null || true
	read -r ans </dev/tty || ans=
	stty echo </dev/tty 2>/dev/null || true
	printf '\n' >/dev/tty
	eval "$1=\$ans"
}

yes_no() { # yes_no VAR "question" default(yes|no)
	ask "$1" "$2 (yes/no)" "$3"
	eval "v=\$$1"
	case $v in y | Y | yes | YES | Yes) eval "$1=yes" ;; *) eval "$1=no" ;; esac
}

detect() {
	OS=$(uname -s)
	case $(uname -m) in
	x86_64 | amd64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) die "unsupported CPU: $(uname -m) (forgerun runs on x86_64 and arm64)" ;;
	esac
	case $OS in
	Darwin)
		[ "$ARCH" = arm64 ] || die "this is an Intel Mac: microVMs need Apple Silicon. Run the worker on an Apple Silicon Mac or a Linux machine with KVM."
		GOOS=darwin
		;;
	Linux) GOOS=linux ;;
	*) die "unsupported system: $OS (forgerun runs on macOS and Linux)" ;;
	esac
	SUDO=
	if [ "$(id -u)" != 0 ]; then
		if have sudo; then SUDO=sudo; fi
	fi
}

# packages installs git, curl, tar and a C compiler (Go builds forgerun with cgo).
packages() {
	if [ "$GOOS" = darwin ]; then
		if ! xcode-select -p >/dev/null 2>&1; then
			xcode-select --install 2>/dev/null || true
			die "installing Apple's Command Line Tools (git and a C compiler): finish it in the window that opened, then run this again"
		fi
		return
	fi
	missing=
	for t in git curl tar gcc make; do have "$t" || missing="$missing $t"; done
	[ -z "$missing" ] && return
	say "installing:$missing"
	if [ "$(id -u)" != 0 ] && [ -z "$SUDO" ]; then die "need root or sudo to install:$missing"; fi
	if have apt-get; then
		$SUDO apt-get update -qq && $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq git curl ca-certificates tar build-essential >/dev/null
	elif have dnf; then
		$SUDO dnf install -y -q git curl ca-certificates tar gcc make
	elif have yum; then
		$SUDO yum install -y -q git curl ca-certificates tar gcc make
	elif have pacman; then
		$SUDO pacman -Sy --noconfirm --needed git curl ca-certificates tar gcc make
	elif have zypper; then
		$SUDO zypper -q install -y git curl ca-certificates tar gcc make
	else
		die "install these with your package manager, then run this again:$missing"
	fi
}

# kvm_access lets this user run microVMs: /dev/kvm belongs to the kvm group.
kvm_access() {
	[ "$GOOS" = linux ] && [ -e /dev/kvm ] || return 0
	[ -r /dev/kvm ] && [ -w /dev/kvm ] && return 0
	[ -n "$SUDO" ] || die "your user can't use /dev/kvm: add it to the kvm group, then log out and back in"
	say "adding $(id -un) to the kvm group (to use /dev/kvm)"
	$SUDO usermod -aG kvm "$(id -un)"
	die "added you to the kvm group: log out and back in (or reboot), then run this again"
}

# source fetches forgerun, or updates the copy that's there.
source_code() {
	if [ -d "$DIR/.git" ]; then
		say "updating $DIR"
		git -C "$DIR" pull -q --ff-only || die "couldn't update $DIR (local changes?): fix it or move it aside, then run this again"
	else
		say "downloading forgerun into $DIR"
		git clone -q --branch "$BRANCH" "$REPO" "$DIR"
	fi
}

# go_tool makes sure Go is there. Go 1.21 or later is enough: it fetches the
# exact version go.mod asks for by itself.
go_tool() {
	PATH=$LOCAL/go/bin:$PATH
	if have go; then
		minor=$(go env GOVERSION 2>/dev/null | sed -n 's/^go1\.\([0-9]*\).*/\1/p')
		if [ -n "$minor" ] && [ "$minor" -ge 21 ]; then return; fi
	fi
	want=$(sed -n 's/^go \([0-9.]*\)$/\1/p' "$DIR/go.mod")
	say "installing Go $want into $LOCAL/go"
	mkdir -p "$LOCAL"
	rm -rf "$LOCAL/go"
	curl -fsSL "https://go.dev/dl/go$want.$GOOS-$ARCH.tar.gz" | tar -C "$LOCAL" -xz
	add_to_path "$LOCAL/go/bin"
}

# node_tool makes sure Node 20+ is there, for building the web UI.
node_tool() {
	PATH=$LOCAL/node/bin:$PATH
	if have node && [ "$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || echo 0)" -ge 20 ]; then return; fi
	case $ARCH in amd64) narch=x64 ;; *) narch=arm64 ;; esac
	base=https://nodejs.org/dist/latest-v22.x
	file=$(curl -fsSL "$base/SHASUMS256.txt" | sed -n "s/.*  \(node-v[0-9.]*-$GOOS-$narch\.tar\.gz\)$/\1/p")
	[ -n "$file" ] || die "couldn't find a Node 22 download for $GOOS-$narch"
	say "installing Node (${file%.tar.gz}) into $LOCAL/node"
	mkdir -p "$LOCAL"
	rm -rf "$LOCAL/node"
	curl -fsSL "$base/$file" | tar -C "$LOCAL" -xz
	mv "$LOCAL/${file%.tar.gz}" "$LOCAL/node"
	add_to_path "$LOCAL/node/bin"
}

add_to_path() {
	profile=$HOME/.profile
	[ "$GOOS" = darwin ] && profile=$HOME/.zprofile
	line="export PATH=\"$1:\$PATH\""
	grep -qsF "$line" "$profile" || printf '\n%s\n' "$line" >>"$profile"
}

build() {
	say "building forgerun"
	if [ "$ROLE" = serve ]; then
		(cd "$DIR" && make build >/dev/null)
	else
		(cd "$DIR" && go build -o forgerun ./cmd/forgerun)
	fi
}

# set_env FILE KEY: writes KEY's current value into FILE as KEY='value',
# replacing its line. Quoted, the file is safe to source; the value goes
# through awk's environment, never a pattern, so no character can break it.
set_env() {
	eval "val=\${$2:-}"
	[ -n "$val" ] || return 0
	case $val in *"'"*) die "$2 contains a single quote: put it in $1 yourself" ;; esac
	tmp="$1.tmp.$$"
	(umask 077 && K="$2" V="'$val'" awk 'BEGIN { k = ENVIRON["K"]; v = ENVIRON["V"] }
		$0 ~ "^" k "=" { if (!done) print k "=" v; done = 1; next } { print }
		END { if (!done) print k "=" v }' "$1" >"$tmp")
	cat "$tmp" >"$1" # rewritten in place: the file keeps its owner-only mode
	rm -f "$tmp"
}

# questions asks everything up front, so the long part runs unattended and
# nothing it runs (apt, say) can swallow an answer typed ahead.
questions() {
	if [ "$ROLE" = worker ]; then
		ENV_FILE=$WORKER_ENV
	else
		ENV_FILE=$DIR/.env
	fi
	if [ -f "$ENV_FILE" ]; then
		NEW_SETTINGS=no
	else
		NEW_SETTINGS=yes
		if [ "$ROLE" = worker ]; then
			ask FORGERUN_SERVER "The orchestrator's address, e.g. https://forgerun.example.com"
			[ -n "$FORGERUN_SERVER" ] || die "a worker needs FORGERUN_SERVER"
			ask_secret FORGERUN_WORKER_TOKEN "Its worker token (the orchestrator's FORGERUN_WORKER_TOKEN)"
			[ -n "${FORGERUN_WORKER_TOKEN:-}" ] || die "a worker needs FORGERUN_WORKER_TOKEN"
		else
			ask_secret GITHUB_TOKEN "A GitHub token that can push to your repos (fine-grained: Contents and Pull requests read/write)"
		fi
		agent_keys
	fi
	yes_no FORGERUN_SERVICE "Run it in the background, now and at every boot?" yes
	say "thanks: the rest runs by itself"
}

configure_worker() {
	if [ "$NEW_SETTINGS" = no ]; then
		say "keeping the settings in $ENV_FILE (edit it to change them)"
		return
	fi
	(umask 077 && : >"$ENV_FILE")
	for k in FORGERUN_SERVER FORGERUN_WORKER_TOKEN CURSOR_API_KEY CLAUDE_CODE_OAUTH_TOKEN OPENCODE_API_KEY; do
		set_env "$ENV_FILE" "$k"
	done
	say "saved the settings to $ENV_FILE (only you can read it)"
}

configure_serve() {
	if [ "$NEW_SETTINGS" = no ]; then
		say "keeping the settings in $ENV_FILE (edit it to change them)"
	else
		(umask 077 && cp "$DIR/.env.example" "$ENV_FILE")
		for k in GITHUB_TOKEN CURSOR_API_KEY CLAUDE_CODE_OAUTH_TOKEN OPENCODE_API_KEY; do
			set_env "$ENV_FILE" "$k"
		done
		say "saved the settings to $ENV_FILE (only you can read it)"
	fi
	if have docker && docker info >/dev/null 2>&1; then
		say "starting Postgres (docker compose)"
		(cd "$DIR" && docker compose up -d >/dev/null)
	else
		warn "Docker isn't running: forgerun keeps its data in Postgres. Install or start Docker, then: cd $DIR && docker compose up -d"
	fi
}

agent_keys() {
	note "Agent keys: each agent needs its own; skip any you don't use."
	ask_secret CURSOR_API_KEY "CURSOR_API_KEY (Cursor)"
	ask_secret CLAUDE_CODE_OAUTH_TOKEN "CLAUDE_CODE_OAUTH_TOKEN (Claude Code: the output of claude setup-token)"
	ask_secret OPENCODE_API_KEY "OPENCODE_API_KEY (OpenCode's paid models; its free ones need none)"
}

check() {
	say "checking the setup (forgerun doctor)"
	# shellcheck disable=SC2086 # the flags are words
	if ! "$DIR/forgerun" doctor -role "$ROLE" -env "$ENV_FILE" ${FORGERUN_DOCTOR_FLAGS:-}; then
		printf '\n'
		die "fix the problems above, then run this installer again (or: $DIR/forgerun doctor -env $ENV_FILE)"
	fi
}

# service runs forgerun in the background, now and at every boot, and
# restarts it if it stops.
service() {
	name=forgerun-$ROLE
	if [ "$GOOS" = darwin ]; then
		label=dev.forgerun.$ROLE
		plist=$HOME/Library/LaunchAgents/$label.plist
		log=$HOME/Library/Logs/$name.log
		mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
		# caffeinate -i keeps the Mac from sleeping while it runs.
		cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$label</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/caffeinate</string><string>-i</string>
		<string>/bin/sh</string><string>-c</string>
		<string>set -a; . "$ENV_FILE"; set +a; exec "$DIR/forgerun" $ROLE</string>
	</array>
	<key>WorkingDirectory</key><string>$DIR</string>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>$log</string>
	<key>StandardErrorPath</key><string>$log</string>
</dict>
</plist>
EOF
		launchctl bootout "gui/$(id -u)/$label" 2>/dev/null || true
		launchctl bootstrap "gui/$(id -u)" "$plist"
		say "running in the background (launchd: $label)"
		note "logs:    tail -f $log"
		note "stop:    launchctl bootout gui/$(id -u)/$label"
		return
	fi
	if ! have systemctl || ! systemctl --user show-environment >/dev/null 2>&1; then
		warn "no systemd user session here: start it yourself with"
		note "set -a && . $ENV_FILE && set +a && $DIR/forgerun $ROLE"
		return
	fi
	unit=$HOME/.config/systemd/user/$name.service
	mkdir -p "$(dirname "$unit")"
	cat >"$unit" <<EOF
[Unit]
Description=forgerun $ROLE
After=network-online.target

[Service]
EnvironmentFile=$ENV_FILE
WorkingDirectory=$DIR
ExecStart="$DIR/forgerun" $ROLE
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
	systemctl --user daemon-reload
	systemctl --user enable -q "$name"
	systemctl --user restart "$name"
	# Keep it running after you log out, and start it at boot.
	loginctl enable-linger "$(id -un)" 2>/dev/null || $SUDO loginctl enable-linger "$(id -un)" 2>/dev/null ||
		warn "couldn't enable lingering: it stops when you log out (sudo loginctl enable-linger $(id -un))"
	say "running in the background (systemd: $name)"
	note "logs:    journalctl --user -u $name -f"
	note "stop:    systemctl --user stop $name"
	if [ -d /proc/acpi/button/lid ] || ls /sys/class/power_supply/BAT* >/dev/null 2>&1; then
		note "on a laptop, keep it from suspending: set HandleLidSwitch=ignore in /etc/systemd/logind.conf"
	fi
}

main() {
	detect
	say "forgerun installer ($GOOS/$ARCH)"
	if [ -z "${FORGERUN_ROLE:-}" ] && tty_ok; then
		printf '\nWhat is this machine for?\n  1) a worker: runs the microVMs for a forgerun server elsewhere\n  2) everything here: the web UI, the database and the VMs\n' >/dev/tty
		ask choice "Choose" 1
		case $choice in 2 | serve) FORGERUN_ROLE=serve ;; *) FORGERUN_ROLE=worker ;; esac
	fi
	ROLE=${FORGERUN_ROLE:-}
	case $ROLE in worker | serve) ;; *) die "set FORGERUN_ROLE to worker or serve" ;; esac

	questions
	packages
	kvm_access
	source_code
	go_tool
	if [ "$ROLE" = serve ]; then node_tool; fi
	build
	if [ "$ROLE" = worker ]; then configure_worker; else configure_serve; fi
	check
	if [ "$FORGERUN_SERVICE" = yes ]; then
		service
	else
		say "to start it: set -a && . $ENV_FILE && set +a && $DIR/forgerun $ROLE"
	fi
	if [ "$ROLE" = serve ]; then note "open http://localhost:8080"; fi
	note "check the setup any time: $DIR/forgerun doctor -env $ENV_FILE"
	note "update: run this installer again"
}

main "$@"
