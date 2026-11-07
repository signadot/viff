# p1: nix 2.35.4, single-user, as 65532, into /nix (an emptyDir standing in for the image's
# /nix). The tarball was unpacked into ~/inst from outside: the image has no xz.
set -eu
export HOME=/home/nonroot/h; mkdir -p $HOME
cd /home/nonroot/inst/nix-*-x86_64-linux
mkdir -p ~/.config/nix
echo "sandbox = false" > ~/.config/nix/nix.conf
s=$(date +%s); ./install --no-daemon --no-modify-profile 2>&1 | tail -5; echo "install: $(( $(date +%s)-s ))s"
. ~/.nix-profile/etc/profile.d/nix.sh
nix --version
echo "nix: $(readlink -f $(command -v nix))"
ls /nix/store | wc -l
