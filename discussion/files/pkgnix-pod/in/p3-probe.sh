# p3 (the local run's 00): Wrap's flags, under hostUsers: false on the node's kernel, with the
# image's bwrap 0.8 and nixpkgs' 0.13.
for BW in $BW8 $BW13; do
  echo "=== $($BW --version)"
  $BW "${BASE[@]}" -- sh -c 'id; grep -E "^(CapEff|NoNewPrivs):" /proc/self/status; echo "uid_map: $(cat /proc/self/uid_map)"; echo inside-ok' 2>&1
  echo "rc=$?"
  echo "--- --remount-ro / after the binds:"
  $BW "${BASE[@]}" --remount-ro / -- sh -c "touch /planted 2>&1 | head -1; touch $WS/w && echo workspace-writable && rm $WS/w; touch /tmp/t && echo tmp-writable" 2>&1
done
