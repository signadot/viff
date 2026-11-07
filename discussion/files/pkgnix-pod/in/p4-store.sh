# p4: the store apart on the gp3 claim (nix --store /state/nix), filled from cache.nixos.org
# from inside the cluster. Each package by the outputs nix would install (meta.outputsToInstall),
# realised by store path, never built.
set -u
mkdir -p $S
APPLY='p: map (o: p.${o}.outPath) (p.meta.outputsToInstall or [ "out" ])'
t0=$(now)
for a in nodejs_22 go python3 git gcc postgresql imagemagick ffmpeg-full rustc cargo jq; do
  s=$(now); outs=$(nix eval --store $S --json "$NP#$a" --apply "$APPLY" 2>/dev/null | tr -d '[]"' | tr ',' ' ')
  e=$(ms $s); s=$(now)
  nix build --store $S --no-link "${SUB[@]}" $outs 2>&1 | tail -2
  r=$(ms $s)
  nix path-info --store $S -r $outs > $CL/$a.paths
  echo "$a: outputs [$(for o in $outs; do echo -n "${o##*-} "; done)] eval=${e}ms realise=${r}ms closure=$(wc -l < $CL/$a.paths)"
  echo $outs > $CL/$a.outs
done
cat $CL/*.paths | sort -u > $CL/ALL.union
echo "total: $(ms $t0)ms; union $(wc -l < $CL/ALL.union) paths; store on disk $(du -sh $S/nix/store | cut -f1); $(df -h /state | tail -1)"
echo "physical: $(ls -d $S/nix/store/* | head -2)"
