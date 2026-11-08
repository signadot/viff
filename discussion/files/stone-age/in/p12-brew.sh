# p12: a working example of the stone-age model on Debian, non-root. The runner has an install
# toolchain (here from nix, standing for what the runner image bakes in: dpkg-deb, xz, patchelf).
# It fetches a package and its library dependencies as .debs (curl, non-root), extracts them
# (dpkg-deb -x, non-root, no maintainer scripts), assembles a self-contained versioned prefix in a
# cellar, and patches each binary's RUNPATH to $ORIGIN/../lib so it finds its own libraries with no
# LD_LIBRARY_PATH and no collision with any other package. Then it materialises the prefix into a
# task's install root $I (a bin symlink farm + the prefix, hardlinked), binds $I read-only into a
# bwrap, and runs the tool as the task. It installs the same tool under a second name to show the
# install-name override (jq and jq-v2 side by side).
set -u
TC=$(nix build --no-link --print-out-paths "${SUB[@]}" $NP#dpkg $NP#xz $NP#zstd $NP#patchelf 2>/dev/null)
export PATH=$(for o in $TC; do printf '%s/bin:' "$o"; done)$PATH
echo "[toolchain] dpkg-deb $(dpkg-deb --version | head -1 | awk '{print $NF}'), xz $(xz --version | head -1 | awk '{print $NF}'), patchelf $(patchelf --version | awk '{print $NF}')"

CELLAR=/state/cellar; rm -rf $CELLAR; mkdir -p $CELLAR
DL=/state/deb; rm -rf $DL; mkdir -p $DL
MIRROR=http://deb.debian.org/debian
# The catalog: resolve each package name to its pool Filename from the suite's Packages index
# (gzip, which the pod can read). This is the "manifest" a `brew update` would refresh.
echo "[catalog] fetching the bookworm Packages index"
curl -sS $MIRROR/dists/bookworm/main/binary-amd64/Packages.gz | gzip -dc > $DL/Packages
filename() { awk -v p="$1" 'BEGIN{RS="";FS="\n"} { f=""; ok=0; for(i=1;i<=NF;i++){ if($i=="Package: "p)ok=1; if($i ~ /^Filename: /){f=substr($i,11)} } if(ok){print f; exit} }' $DL/Packages; }
ver()      { awk -v p="$1" 'BEGIN{RS="";FS="\n"} { v=""; ok=0; for(i=1;i<=NF;i++){ if($i=="Package: "p)ok=1; if($i ~ /^Version: /){v=substr($i,10)} } if(ok){print v; exit} }' $DL/Packages; }

# install_pkg <cellar-name> <version-label> <debian-pkg> [more-debian-pkgs for libs...]
# Fetch the named debs, extract them into one staging tree, lift the binaries into <prefix>/bin and
# the package's own shared libraries into <prefix>/lib, and set each binary's RUNPATH to its own
# lib dir. libc and the loader are the base system's, found as usual; only the package's libraries
# are bundled, so two packages never share a lib path.
install_pkg() {
  local name=$1 verl=$2; shift 2
  local prefix=$CELLAR/$name/$verl stage=$DL/$name-$verl; rm -rf $stage; mkdir -p $stage $prefix/bin $prefix/lib
  for p in "$@"; do
    local fn=$(filename "$p"); [ -n "$fn" ] || { echo "[install] $p not in the index"; return 1; }
    curl -sS -o $DL/$p.deb $MIRROR/$fn
    dpkg-deb -x $DL/$p.deb $stage
  done
  # the executables the first package ships in a bin dir
  for b in $stage/usr/bin/* $stage/bin/*; do [ -f "$b" ] && [ -x "$b" ] && cp -a "$b" $prefix/bin/; done 2>/dev/null
  # every shared object from the extracted libs, real files AND the soname symlinks the binary
  # actually names (libjq.so.1 -> libjq.so.1.0.4); cp -a keeps the links, relative within lib/
  find $stage \( -name '*.so' -o -name '*.so.*' \) -exec cp -an {} $prefix/lib/ \; 2>/dev/null
  # self-contain each binary: its libs are a sibling ../lib
  for b in $prefix/bin/*; do patchelf --set-rpath '$ORIGIN/../lib' "$b" 2>/dev/null; done
  # and each library finds its own siblings ($ORIGIN): DT_RUNPATH does not reach a transitive
  # dependency, so libjq must carry its own path to find libonig beside it. Patch real files only.
  for so in $prefix/lib/*; do [ -f "$so" ] && [ ! -L "$so" ] && patchelf --set-rpath '$ORIGIN' "$so" 2>/dev/null; done
  echo "[install] $name $verl: bin [$(ls $prefix/bin | tr '\n' ' ')] lib [$(ls $prefix/lib | tr '\n' ' ')]"
}

JQV=$(ver jq)
echo "[catalog] jq resolves to version $JQV"
install_pkg jq "$JQV" jq libjq1 libonig5

echo "[runner] run the cellar binary directly, NO LD_LIBRARY_PATH (RUNPATH makes it self-contained):"
echo "   $CELLAR/jq/$JQV/bin/jq --version -> $($CELLAR/jq/$JQV/bin/jq --version 2>&1)"
echo "   its RUNPATH: $(patchelf --print-rpath $CELLAR/jq/$JQV/bin/jq); needs: $(ldd $CELLAR/jq/$JQV/bin/jq | grep -E 'libjq|libonig' | tr '\n' ' ')"

# Materialise into a task's install root. opt/<name>-<ver> is the prefix (hardlinked from the
# cellar, near-free, same volume; a running task keeps it even if the cellar entry is evicted). bin
# is a symlink farm; a relative link so it resolves wherever $I is bound. The install-name override
# is just the link's name: jq and jq-v2 both point at the one prefix.
materialise() { # $I <name> <version> <command> <as>
  local I=$1 name=$2 ver=$3 cmd=$4 as=$5 nv=$2-$3
  mkdir -p $I/bin $I/opt
  [ -e $I/opt/$nv ] || cp -al $CELLAR/$name/$ver $I/opt/$nv
  ln -sf ../opt/$nv/bin/$cmd $I/bin/$as
}
I=/state/tasks/i2; rm -rf $I; mkdir -p $I
materialise $I jq "$JQV" jq jq
materialise $I jq "$JQV" jq jq-v2     # the same prefix, a second name (the override)
echo "[runner] the task root: bin [$(ls $I/bin | tr '\n' ' ')] -> $(for l in $I/bin/*; do echo -n "$(basename $l)=>$(readlink $l) "; done)"

echo "[runner] --- run them inside a bwrap task, \$I bound read-only at /opt, PATH=/opt/bin"
$BW13 "${BASE[@]}" --ro-bind $I /opt --setenv PATH /opt/bin:/usr/bin:/bin --remount-ro / -- bash -c '
  echo "[task] uid $(id -u) CapEff $(grep CapEff /proc/self/status | cut -f2); PATH=$PATH"
  echo "[task] which jq: $(command -v jq); which jq-v2: $(command -v jq-v2)"
  echo "[task] jq --version:    $(jq --version 2>&1)"
  echo "[task] jq-v2 --version: $(jq-v2 --version 2>&1)"
  echo "[task] a real run: echo {\"a\":1} | jq .a -> $(echo "{\"a\":1}" | jq .a 2>&1)"
  echo "[task] no LD_LIBRARY_PATH set: [${LD_LIBRARY_PATH:-unset}]"
  echo "[task] write into /opt: $(touch /opt/bin/evil 2>&1 | head -1 | cut -c1-60)"
  echo "[task] can it see the cellar or other tasks? /state: $(ls /state 2>&1 | head -1)"
'
echo "[runner] done; cellar holds versions: $(find $CELLAR -maxdepth 2 -mindepth 2 | sed 's#.*/cellar/##' | tr '\n' ' ')"
