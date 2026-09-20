# up: delete the wait-for-docd loop, so an unreachable store at boot is the steady state's first iteration

cmd/verse/up.go's openDocdStore has its own deadline, retry loop and 'docd … not ready yet, waiting…' line. The store session re-establishes itself when the wire goes quiet; boot should be that same path rather than a second mechanism with its own patience.

Step 4 of docs/working/work/store-session-plan.md, whose other steps landed or were decided the other way.