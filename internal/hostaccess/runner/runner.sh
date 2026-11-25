# machinarchy detached runner, protocol 1 (AUDIT/10b §1.2).
#
# Sent as the argument of `sh -c` with every SSH session; nothing persists on
# the host except one state directory per target:
#
#   ROOT/ops/OPERATION/TARGET/   mode 0700
#     cmd      the command (already shell-quoted by machinarchy)
#     meta     runner version, mode, unit, start time
#     wrapper  the supervisor script
#     pid      the wrapper's pid (written by the wrapper)
#     pgid     process group of the command, or `child` (pid) without one
#     stdout   stderr   appended by the command
#     exit     exit status, written to exit.tmp and renamed (atomic)
#     timedout cancelled   markers
#
# Every protocol line starts with "@@MR1 " so output from shell start-up
# files is ignored. Arguments are positional; nothing is interpolated into
# a command line except validated identifiers.
set -u
umask 077
PATH=${PATH:-}:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

say() { printf '@@MR1 %s\n' "$*"; }
fail() { say "error $1"; exit 3; }

valid_id() {
	case $1 in '' | *[!a-z0-9-]*) return 1 ;; esac
	[ ${#1} -le 64 ]
}
valid_num() {
	case $1 in '' | *[!0-9]*) return 1 ;; esac
	[ ${#1} -le 12 ]
}

# root ROOT: the state root; the default lives under the login user's home.
root_dir() {
	if [ -z "$1" ]; then
		[ -n "${HOME:-}" ] || fail no_home
		ROOT="$HOME/.local/state/machinarchy"
		return
	fi
	case $1 in /*) ;; *) fail bad_root ;; esac
	case $1 in *..* | *[!A-Za-z0-9._/-]*) fail bad_root ;; esac
	ROOT=$1
}

# ref OP/TARGET -> D (the checked target directory)
target_dir() {
	op=${1%%/*}
	t=${1#*/}
	valid_id "$op" || fail bad_id
	valid_id "$t" || fail bad_id
	D="$ROOT/ops/$op/$t"
}

now() { date +%s; }

# alive PID MARK: the process exists and its command line contains MARK, so
# a reused pid is not mistaken for the wrapper.
alive() {
	if [ -r "/proc/$1/cmdline" ]; then
		tr '\000' ' ' <"/proc/$1/cmdline" 2>/dev/null | grep -F -q -- "$2"
		return
	fi
	ps -o args= -p "$1" 2>/dev/null | grep -F -q -- "$2"
}

# kill_tree SIG PID: signal PID and all its descendants.
kill_tree() {
	sig=$1
	todo=$2
	all=
	while [ -n "$todo" ]; do
		set -- $todo
		p=$1
		shift
		todo=$*
		all="$all $p"
		kids=$( (ps -A -o pid= -o ppid= 2>/dev/null || ps -o pid= -o ppid=) | awk -v p="$p" '$2 == p { print $1 }')
		todo="$todo $kids"
	done
	for p in $all; do kill -"$sig" "$p" 2>/dev/null; done
}

# kill_target SIG DIR: signal the command's process group (or tree).
kill_target() {
	if [ -f "$2/pgid" ]; then
		kill -"$1" -"$(cat "$2/pgid")" 2>/dev/null
	elif [ -f "$2/child" ]; then
		kill_tree "$1" "$(cat "$2/child")"
	fi
}

# state DIR -> STATE, CODE
target_state() {
	CODE=-
	if [ ! -d "$1" ]; then
		STATE=missing
	elif [ -f "$1/exit" ]; then
		STATE=exited
		CODE=$(cat "$1/exit")
	elif [ -f "$1/pid" ] && alive "$(cat "$1/pid")" "$1/wrapper"; then
		STATE=running
	elif [ -f "$1/exit" ]; then
		STATE=exited
		CODE=$(cat "$1/exit")
	elif [ ! -f "$1/meta" ]; then
		# The launch never got as far as starting the command.
		lp=$(cat "$1/launching" 2>/dev/null)
		if [ -n "$lp" ] && alive "$lp" machinarchy-runner; then
			STATE=running # the launch is in progress
		else
			STATE=missing
		fi
	elif [ ! -f "$1/pid" ] &&
		[ $(($(now) - $(sed -n 's/^started=//p' "$1/meta"))) -lt 60 ]; then
		STATE=running # the supervisor is still starting
	else
		STATE=lost
	fi
}

write_wrapper() {
	cat >"$1/wrapper" <<'WRAPPER'
# machinarchy supervisor, protocol 1: DIR TIMEOUT BLOCKS
d=$1
timeout=$2
blocks=$3
umask 077
trap '' HUP
echo $$ >"$d/pid.tmp" && mv "$d/pid.tmp" "$d/pid"
finish() {
	printf '%s\n' "$1" >"$d/exit.tmp" && mv "$d/exit.tmp" "$d/exit"
	exit 0
}
[ -e "$d/cancelled" ] && finish 130
if command -v setsid >/dev/null 2>&1; then
	(ulimit -f "$blocks" 2>/dev/null; exec setsid /bin/sh "$d/cmd") </dev/null >>"$d/stdout" 2>>"$d/stderr" &
	child=$!
	grouped=1
	echo "$child" >"$d/pgid"
else
	set -m 2>/dev/null
	(ulimit -f "$blocks" 2>/dev/null; exec /bin/sh "$d/cmd") </dev/null >>"$d/stdout" 2>>"$d/stderr" &
	child=$!
	set +m 2>/dev/null
	if [ "$(ps -o pgid= -p "$child" 2>/dev/null | tr -d ' ')" = "$child" ]; then
		grouped=1
		echo "$child" >"$d/pgid"
	else
		echo "$child" >"$d/child"
	fi
fi
# The process group is remembered, not re-read from $d: the directory may be
# gone. The tree walk stops when no pid is left; the list always carries a
# separator, so testing it for emptiness looped forever (AUDIT/20 F20-07).
signal() {
	s=$1
	if [ -n "${grouped:-}" ]; then
		kill -"$s" -"$child" 2>/dev/null
	else
		todo=$child
		all=
		while :; do
			set -- $todo
			[ "$#" -gt 0 ] || break
			p=$1
			shift
			todo=$*
			all="$all $p"
			todo="$todo $( (ps -A -o pid= -o ppid= 2>/dev/null || ps -o pid= -o ppid=) | awk -v p="$p" '$2 == p { print $1 }')"
		done
		for p in $all; do kill -"$s" "$p" 2>/dev/null; done
	fi
}
(
	waited=0
	while [ "$waited" -lt "$timeout" ]; do
		step=$((timeout - waited))
		[ "$step" -gt 5 ] && step=5
		sleep "$step"
		waited=$((waited + step))
		[ -e "$d/exit" ] && exit 0
	done
	# printf, not ':': a failed redirection on a special built-in ends the
	# shell, and the command would then never be signalled (F20-07).
	printf '' >"$d/timedout" 2>/dev/null
	signal TERM
	sleep 10
	[ -e "$d/exit" ] || signal KILL
) </dev/null >/dev/null 2>&1 &
wait "$child"
finish $?
WRAPPER
}

systemd_scope() {
	command -v systemd-run >/dev/null 2>&1 || return 1
	if [ "$(id -u)" = 0 ]; then
		case $(systemctl is-system-running 2>/dev/null) in
		running | degraded | starting) SCOPE=system; return 0 ;;
		esac
		return 1
	fi
	# A user manager stops with the last session unless lingering is on,
	# and our session ends right after the launch.
	loginctl show-user "$(id -un)" -p Linger 2>/dev/null | grep -q '=yes' || return 1
	systemctl --user show-environment >/dev/null 2>&1 || return 1
	SCOPE=user
}

report_launch() {
	mode=$(sed -n 's/^mode=//p' "$1/meta" 2>/dev/null)
	unit=$(sed -n 's/^unit=//p' "$1/meta" 2>/dev/null)
	pid=$(cat "$1/pid" 2>/dev/null)
	say "launched ${mode:--} ${pid:--} ${unit:--} $2"
}

# sweep DAYS: remove finished or lost directories older than DAYS.
sweep() {
	[ -d "$ROOT/ops" ] || return 0
	find "$ROOT/ops" -mindepth 2 -maxdepth 2 -type d -mtime +"$1" 2>/dev/null | while read -r old; do
		target_state "$old"
		[ "$STATE" = running ] || rm -rf -- "$old"
	done
	find "$ROOT/ops" -mindepth 1 -maxdepth 1 -type d -empty -exec rmdir {} + 2>/dev/null
	return 0
}

# launch ROOT OP TARGET TIMEOUT MAXBYTES MODE RETENTION_DAYS COMMAND
do_launch() {
	root_dir "$1"
	target_dir "$2/$3"
	timeout=$4 maxb=$5 mode=$6 days=$7 cmd=$8
	valid_num "$timeout" && valid_num "$maxb" && valid_num "$days" || fail bad_number
	case $mode in auto | systemd | setsid) ;; *) fail bad_mode ;; esac
	mkdir -p "$ROOT/ops/$op" || fail mkdir
	chmod 700 "$ROOT" "$ROOT/ops" "$ROOT/ops/$op" 2>/dev/null
	if ! mkdir "$D" 2>/dev/null; then
		[ -d "$D" ] || fail mkdir
		# Already launched (a retry after a lost reply): adopt it.
		if [ -f "$D/meta" ]; then
			report_launch "$D" existing
			return
		fi
		# No meta: a launch died before it started anything, or one is in
		# progress right now. meta is written before the start, so a
		# directory without it never ran the command.
		lp=$(cat "$D/launching" 2>/dev/null)
		if [ -n "$lp" ] && alive "$lp" machinarchy-runner; then
			fail busy
		fi
		rm -rf -- "$D"
		mkdir "$D" 2>/dev/null || fail busy
	fi
	echo $$ >"$D/launching"
	printf '%s\n' "$cmd" >"$D/cmd"
	: >"$D/stdout"
	: >"$D/stderr"
	write_wrapper "$D"
	blocks=$(((maxb + 511) / 512))
	started=$(now)
	unit="machinarchy-$op-$t"
	SCOPE=
	if [ "$mode" != setsid ] && systemd_scope; then
		printf 'version=1\nmode=systemd\nunit=%s\nscope=%s\nstarted=%s\n' "$unit" "$SCOPE" "$started" >"$D/meta"
		flag=
		[ "$SCOPE" = user ] && flag=--user
		if systemd-run $flag --unit="$unit" --collect --quiet -p RuntimeMaxSec=$((timeout + 60)) \
			-p KillMode=control-group /bin/sh "$D/wrapper" "$D" "$timeout" "$blocks" >/dev/null 2>&1; then
			mode=systemd
		elif [ "$mode" = systemd ]; then
			rm -rf -- "$D"
			fail systemd_failed
		else
			mode=setsid
		fi
	elif [ "$mode" = systemd ]; then
		rm -rf -- "$D"
		fail systemd_unavailable
	else
		mode=setsid
	fi
	if [ "$mode" = setsid ]; then
		printf 'version=1\nmode=setsid\nunit=\nstarted=%s\n' "$started" >"$D/meta"
		if command -v setsid >/dev/null 2>&1; then
			setsid /bin/sh "$D/wrapper" "$D" "$timeout" "$blocks" </dev/null >/dev/null 2>&1 &
		else
			nohup /bin/sh "$D/wrapper" "$D" "$timeout" "$blocks" </dev/null >/dev/null 2>&1 &
		fi
	fi
	i=0
	while [ ! -f "$D/pid" ] && [ $i -lt 50 ]; do
		sleep 0.1 2>/dev/null || sleep 1
		i=$((i + 1))
	done
	report_launch "$D" new
	sweep "$days"
}

# chunk FILE FROM CHUNK HEAD -> "SIZE FROM BASE64"
chunk() {
	size=$(wc -c <"$1" 2>/dev/null | tr -d ' ')
	size=${size:-0}
	from=$2
	[ "$from" -gt "$size" ] && from=$size
	# Past the head, skip to the last CHUNK bytes rather than fall behind.
	if [ "$from" -ge "$4" ] && [ $((size - from)) -gt "$3" ]; then
		from=$((size - $3))
	fi
	n=$((size - from))
	[ "$n" -gt "$3" ] && n=$3
	data=-
	if [ "$n" -gt 0 ]; then
		data=$(tail -c +$((from + 1)) "$1" | head -c "$n" | base64 | tr -d '\n')
	fi
	printf '%s %s %s' "$size" "$from" "$data"
}

# status ROOT CHUNK HEAD OP/TARGET:OUTFROM:ERRFROM...
do_status() {
	root_dir "$1"
	chunkb=$2 head=$3
	valid_num "$chunkb" && valid_num "$head" || fail bad_number
	shift 3
	for q in "$@"; do
		ref=${q%%:*}
		rest=${q#*:}
		outfrom=${rest%%:*}
		errfrom=${rest#*:}
		valid_num "$outfrom" && valid_num "$errfrom" || fail bad_number
		target_dir "$ref"
		target_state "$D"
		flags=
		[ -e "$D/timedout" ] && flags="${flags}timedout,"
		[ -e "$D/cancelled" ] && flags="${flags}cancelled,"
		flags=${flags%,}
		if [ "$STATE" = missing ]; then
			say "status $ref missing - - 0 0 - 0 0 -"
			continue
		fi
		version=$(sed -n 's/^version=//p' "$D/meta" 2>/dev/null)
		if [ "${version:-1}" != 1 ]; then
			say "status $ref unsupported - - 0 0 - 0 0 -"
			continue
		fi
		say "status $ref $STATE $CODE ${flags:--} $(chunk "$D/stdout" "$outfrom" "$chunkb" "$head") $(chunk "$D/stderr" "$errfrom" "$chunkb" "$head")"
	done
}

# cancel ROOT OP/TARGET...: TERM now, KILL after 10 s.
do_cancel() {
	root_dir "$1"
	shift
	for ref in "$@"; do
		target_dir "$ref"
		if [ ! -d "$D" ] || [ -f "$D/exit" ]; then
			say "cancel $ref done"
			continue
		fi
		: >"$D/cancelled"
		kill_target TERM "$D"
		(
			sleep 10
			[ -e "$D/exit" ] || kill_target KILL "$D"
		) </dev/null >/dev/null 2>&1 &
		say "cancel $ref sent"
	done
}

# cleanup ROOT OP/TARGET...: remove finished or lost target directories.
do_cleanup() {
	root_dir "$1"
	shift
	for ref in "$@"; do
		target_dir "$ref"
		target_state "$D"
		if [ "$STATE" = running ]; then
			say "cleanup $ref busy"
			continue
		fi
		rm -rf -- "$D"
		rmdir -- "$ROOT/ops/$op" 2>/dev/null
		say "cleanup $ref done"
	done
}

action=${1:-}
[ $# -gt 0 ] && shift
case $action in
launch) [ $# -eq 8 ] || fail usage; do_launch "$@" ;;
status) [ $# -ge 3 ] || fail usage; do_status "$@" ;;
cancel) [ $# -ge 1 ] || fail usage; do_cancel "$@" ;;
cleanup) [ $# -ge 1 ] || fail usage; do_cleanup "$@" ;;
*) fail usage ;;
esac
