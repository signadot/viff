# iod: the binary's default `-as` is agent:<subcommand>-iod, and `verse iod add` grants agent:<root>-iod

cmd/iod/main.go:178 defaults an iod's principal to `"agent:" + connector + "-iod"`, the subcommand's name. `verse iod add` (cmd/verse/io.go:230) defaults the principal it writes grants for to `"agent:" + root + "-iod"`, the root's. With a `-root` other than the subcommand's name and no `-as`, the grants are written for one principal and the iod acts as another, so its writes are refused.

Found by the docs pass (ybved3g6h12ksffvw9n0); docs/reference/iod.md now says what each does. Which default is right is a code decision.