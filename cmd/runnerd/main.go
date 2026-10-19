// Command runnerd runs one of the runners verse ships: a program that does the work a launch
// describes, and that verse reaches over a URL.
//
//	runnerd verse -config verse-runner.tony   # verse's own agent, and scripts
//
// Each task runs in a process of its own, confined by bwrap unless -sandbox none says otherwise.
//
// Then register it, from anywhere that can reach verse:
//
//	verse runner add verse-runner http://localhost:9802
//
// WHY THIS IS A SEPARATE BINARY, for the reason cmd/iod gives: a runner is a program somebody
// deploys, and verse doesn't have to be built with one. A runner registered from elsewhere is
// no different from one shipped here.
package main

import (
	"fmt"
	"log"
	"os"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("runnerd: ")
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "verse":
		err = verseMain(args)
	case "-h", "--help", "help":
		usage()
	default:
		log.Printf("no such runner %q", cmd)
		usage()
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `runnerd runs one runner verse ships.

  runnerd verse -config <file> [-sandbox bwrap|none]
                                 verse's own agent, and scripts (PORTKEY_API_KEY)

`)
	os.Exit(2)
}
