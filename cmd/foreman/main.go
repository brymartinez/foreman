package main

import (
	"fmt"
	"os"

	"github.com/brymartinez/foreman/internal/cli"
)

var stageFor = map[cli.Name]int{
	cli.Inbox:   1,
	cli.Status:  1,
	cli.Show:    1,
	cli.Start:   2,
	cli.Respond: 3,
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	cmd, err := cli.Parse(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, cli.Usage)
		return 64
	}
	if cmd.Name == cli.Help {
		fmt.Print(cli.Usage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "foreman %s: not implemented yet (Stage %d)\n", cmd.Name, stageFor[cmd.Name])
	return 2
}
