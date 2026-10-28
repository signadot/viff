# gofmt: credentials_test.go's refusal table is out of alignment after the rebase onto main

The rebase of verse/verse-system-base onto main put main's three shellEnv rows (2ad65c899) beside the branch's rows in runner/runnerd/stock/credentials_test.go's refusal table, and gofmt aligns the map's values differently with all of them in. Format only.