# p13 (0d): can apt resolve a package's dependency closure as non-root (uid 65532), against the
# Debian index, with the runner image's own dpkg status as the base-provides set so the closure is
# only the delta (not libc and friends the image already has)? p12 proved fetch+extract+run on a
# hand-listed deb set; this is the piece it did not prove: automated resolution.
set -u
echo "=== who, and what is here ==="
id; for t in apt-get dpkg gpgv; do printf '%s: ' $t; command -v $t || echo missing; done
echo "keyrings: $(ls /etc/apt/trusted.gpg.d/ 2>/dev/null | tr '\n' ' ')$(ls /usr/share/keyrings/ 2>/dev/null | tr '\n' ' ')"
echo "the image's installed base (the provides set): $(dpkg-query -f '${Package}\n' -W 2>/dev/null | wc -l) packages, e.g. $(dpkg-query -f '${Package} ' -W libc6 libc-bin 2>/dev/null)"

U=/state/apt; rm -rf $U; mkdir -p $U/lists/partial $U/archives/partial
echo "deb http://deb.debian.org/debian bookworm main" > $U/sources.list
# Private state and cache (writable by 65532); the REAL dpkg status (read), so the resolver treats
# the image's packages as installed and returns only what a tool adds; no privilege drop for the
# fetch (no _apt user here).
O="-o Dir::State::Lists=$U/lists -o Dir::Cache::Archives=$U/archives
   -o Dir::Etc::SourceList=$U/sources.list -o Dir::Etc::SourceParts=/dev/null
   -o Dir::State::status=/var/lib/dpkg/status -o APT::Sandbox::User=
   -o Acquire::Check-Valid-Until=false"

echo "=== apt-get update, non-root, signatures checked ==="
apt-get $O update 2>&1 | tail -6
echo "update exit: $?"

echo "=== resolve jq's closure (simulate), minus the installed base ==="
apt-get $O install -s --no-install-recommends jq 2>&1 | grep -E '^(Inst|Conf|E:|W:)' | head -40

echo "=== does it correctly SUBTRACT the base (no libc6/libc-bin/libm in the closure)? ==="
if apt-get $O install -s --no-install-recommends jq 2>&1 | grep -E '^Inst (libc6|libc-bin) '; then echo "  -> base NOT subtracted"; else echo "  -> base subtracted (libc etc. not in the closure)"; fi

echo "=== the fetch plan: urls + hashes for the closure (--print-uris) ==="
apt-get $O install -y --no-install-recommends --print-uris jq 2>&1 | grep -E "^'http" | head -40

echo "=== a bigger tool, to see real resolution: ripgrep, and git ==="
for pkg in ripgrep git; do
  echo "-- $pkg closure:"
  apt-get $O install -s --no-install-recommends $pkg 2>&1 | grep -E '^Inst' | awk '{print $2}' | tr '\n' ' '; echo
done
