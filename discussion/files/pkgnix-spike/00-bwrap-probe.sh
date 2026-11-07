#!/bin/sh
# Q0: does plain bwrap, with BwrapSandbox.Wrap's flags, run as uid 65532 with all caps dropped,
# and which docker security options does it need? Each variant is one fresh container.
IMG=pkgnix-spike:local
BW='mkdir -p /tmp/ws && bwrap --die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts --proc /proc --dev /dev --tmpfs /tmp --ro-bind-try /usr /usr --ro-bind-try /bin /bin --ro-bind-try /sbin /sbin --ro-bind-try /lib /lib --ro-bind-try /lib64 /lib64 --ro-bind-try /etc /etc --ro-bind-try /opt /opt --bind /home/nonroot /home/nonroot --chdir /home/nonroot --unshare-net -- sh -c "id; echo inside-ok; cat /proc/self/status | grep -E \"^(CapEff|NoNewPrivs|Seccomp):\""'
BASE="--rm --platform linux/arm64 --user 65532 --cap-drop ALL --security-opt no-new-privileges"
v() { echo "=== variant: $1"; shift; docker run $BASE "$@" $IMG sh -c "$BW" 2>&1; echo "exit=$?"; }
echo "bwrap version:"; docker run --rm $IMG bwrap --version
echo "host-of-container kernel:"; docker run --rm $IMG sh -c 'uname -a; cat /proc/sys/user/max_user_namespaces /proc/sys/fs/mount-max 2>&1; cat /proc/sys/kernel/unprivileged_userns_clone 2>&1; ls /sys/kernel/security/ 2>&1 | head'
v "default seccomp (none of ours)"
v "seccomp=unconfined" --security-opt seccomp=unconfined
v "seccomp=unconfined + systempaths=unconfined (~procMount: Unmasked)" --security-opt seccomp=unconfined --security-opt systempaths=unconfined
v "seccomp=unconfined + systempaths=unconfined + apparmor=unconfined" --security-opt seccomp=unconfined --security-opt systempaths=unconfined --security-opt apparmor=unconfined
v "systempaths=unconfined, default seccomp" --security-opt systempaths=unconfined
