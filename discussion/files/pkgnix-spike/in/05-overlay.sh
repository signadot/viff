# Q5 (in the pod): option 4. The task's /nix is an overlay made by bwrap, unprivileged:
# lower = a doer's base store (a separate store, option-3 style, at /home/nonroot/s3/nix),
# upper = a per-task dir. The task runs nix itself, single-user, and substitutes into its layer.
set -u
BW=$(command -v bwrap-new || command -v bwrap)
export NIX_CONFIG="sandbox = false
experimental-features = nix-command flakes"
P=github:NixOS/nixpkgs/$(cat /home/nonroot/nixpkgs.rev)
LOWER=/home/nonroot/s3/nix
NIXBIN=$(readlink -f $(command -v nix)); NIXPKG=${NIXBIN%/bin/nix}
echo "bwrap: $($BW --version); nix package: $NIXPKG"
echo "--- put nix itself into the base store (nix copy into the local chroot store)"
nix copy --no-check-sigs --to /home/nonroot/s3 "$NIXPKG" 2>&1 | tail -1; echo "base store now: $(ls $LOWER/store | grep -vc '\.drv$') non-drv paths"
RG=$(nix eval --raw "$P#ripgrep.outPath"); echo "target (not in the base): $RG ; in base? $(ls -d /home/nonroot/s3$RG 2>/dev/null || echo no)"
GO=$(grep -E -- '-go-1[^/]*$' /home/nonroot/closures/go.paths)
clean() { [ -e "$1" ] && find "$1" -type d -exec chmod u+w {} + 2>/dev/null; rm -rf "$1"; }  # dirs only: an upper's files are its own copies, but keep the habit
O=/home/nonroot/ovl/t1; clean /home/nonroot/ovl; mkdir -p $O/upper $O/work /home/nonroot/ovl/ws
BASE=(--die-with-parent --new-session --cap-drop ALL --unshare-pid --unshare-ipc --unshare-uts
      --proc /proc --dev /dev --tmpfs /tmp)
for d in /usr /bin /sbin /lib /lib64 /etc /opt; do BASE+=(--ro-bind-try $d $d); done
BASE+=(--bind /home/nonroot/ovl/ws /home/nonroot/ovl/ws --chdir /home/nonroot/ovl/ws)   # network open: it must reach the cache
OV=(--overlay-src $LOWER --overlay $O/upper $O/work /nix)
s=$(date +%s%N)
$BW "${BASE[@]}" "${OV[@]}" --setenv HOME /tmp --setenv NIX_CONFIG "$NIX_CONFIG" -- sh -c "
  echo '[task] /nix mount:' \$(grep ' /nix ' /proc/self/mountinfo | grep -o 'overlay.*' | cut -c1-60)
  echo '[task] base go runs:' \$($GO/bin/go version)
  echo '[task] nix-store --realise of ripgrep (substitute, max-jobs 0):'
  $NIXPKG/bin/nix-store --option max-jobs 0 --realise $RG 2>&1 | tail -2
  $RG/bin/rg --version | head -1
  echo '[task] nix path-info sees base and own:' \$($NIXPKG/bin/nix path-info $GO $RG 2>&1 | wc -l) 'of 2'
  echo '[task] try to write a base path:'; chmod u+w $GO 2>&1 && echo '  chmod ok (copy-up into the upper only?)' 
" 2>&1
e=$(date +%s%N); echo "sandbox wall: $(( (e-s)/1000000 ))ms"
echo "--- after the task:"
echo "upper holds: $(ls $O/upper/store 2>/dev/null | tr '\n' ' ')"
echo "upper size: $(du -sh $O/upper | cut -f1); db copied up: $(ls -la $O/upper/var/nix/db/db.sqlite 2>&1 | awk '{print $5, $9}')"
echo "base untouched? ripgrep in lower: $(ls -d /home/nonroot/s3$RG 2>/dev/null || echo no); base go mode: $(stat -c %a /home/nonroot/s3$GO)"
echo "--- --tmp-overlay variant (upper on a tmpfs, gone at exit):"
$BW "${BASE[@]}" --overlay-src $LOWER --tmp-overlay /nix --setenv HOME /tmp --setenv NIX_CONFIG "$NIX_CONFIG" -- sh -c "
  $NIXPKG/bin/nix-store --option max-jobs 0 --realise $RG >/dev/null 2>&1 && $RG/bin/rg --version | head -1" 2>&1
echo "--- the same task with --unshare-net (no route to the cache):"
clean $O; mkdir -p $O/upper $O/work
$BW "${BASE[@]}" --unshare-net "${OV[@]}" --setenv HOME /tmp --setenv NIX_CONFIG "$NIX_CONFIG
connect-timeout = 3" -- sh -c "$NIXPKG/bin/nix-store --option max-jobs 0 --realise $RG 2>&1 | tail -2" 2>&1
clean /home/nonroot/ovl; echo "cleaned: $(ls /home/nonroot/ovl 2>&1)"
