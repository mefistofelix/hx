package main

import (
	"os"

	hxlib "hx/src/hx"
)

func main() {
	os.Exit(hxlib.Main(os.Args, os.Stdout, os.Stderr))
}
