package main

import (
	"os"

	"github.com/fingon/lima-jailed-agents"
)

func main() {
	os.Exit(lja.Main(os.Args[1:]))
}
