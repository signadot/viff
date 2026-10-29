# versed: retire arms a wall-clock timer, not the server's clock

`Server.retire` (versed/server.go) calls `time.AfterFunc` directly. Every other timer in versed goes through `s.clock.AfterFunc`, so a test moves the clock rather than waiting on it. The retention is the one timer the fake clock cannot drive: launchstate_test sets `HostedRetention` to 20ms and sleeps real time.

Use the server's clock. The retention tests then advance the fake clock and assert.