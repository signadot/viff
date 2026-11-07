# p0: what the pod is. Identity, kernel, bwrap, the tools the image has, the volumes.
id; uname -a
echo "bwrap: $(bwrap --version)"
echo "max_user_namespaces=$(cat /proc/sys/user/max_user_namespaces) mount-max=$(cat /proc/sys/fs/mount-max)"
echo "uid_map: $(cat /proc/self/uid_map)"
grep -E '^(CapEff|NoNewPrivs|Seccomp):' /proc/self/status
for t in xz tar gzip curl git python3 time; do printf '%s: ' $t; command -v $t || echo missing; done
nproc; grep -E 'MemTotal' /proc/meminfo
for m in /nix /home/nonroot /state; do echo "--- $m"; stat -c '%U:%G %a' $m; grep " $m " /proc/self/mountinfo | cut -d' ' -f4-11; df -h $m | tail -1; done
touch /state/w && rm /state/w && echo "/state writable"
