package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type Command struct {
	Original    string   `json:"original"`
	Substituted string   `json:"substituted"`
	Arg         string   `json:"arg"`
	Stdout      []string `json:"stdout"`
	//Stdin       string    `json:"stdin"`
	Stderr     []string  `json:"stderr"`
	StartTime  time.Time `json:"starttime"`
	EndTime    time.Time `json:"endtime"`
	RunTime    string    `json:"runtime"`
	ReturnCode int       `json:"returncode"`
}

type Flags struct {
	Any             bool
	All             bool
	ConcurrentLimit int
	Timeout         time.Duration
	Token           string
	FlagErrors      bool
	FirstZero       bool
}

type CommandList []*Command

func (c Command) String() string {
	b, _ := json.MarshalIndent(c, "", " ") // TODO clean this up, particularly RunTime
	return string(b)

}

var flagErrors bool

func Do(command string, substituteArgs []string, flags Flags) {
	// do all the heavy lifting here

	flagErrors = flags.FlagErrors
	systemStartTime := time.Now()

	// TODO: pass flags in as float in seconds, convert to integer msec
	t := time.Duration(flags.Timeout) * time.Millisecond
	ctx, cancelCtx := context.WithTimeout(context.Background(), t)
	defer cancelCtx()

	// build a list of commands
	cmdList, err := buildListOfCommands(command, substituteArgs, flags.Token)
	if err != nil {
		panic(err) // TODO fix
	}

	// go run the things
	completedCommands := start_command_loop(ctx, cmdList, flags)

	// presentation and cleanup
	systemEndTime := time.Now()
	systemRunTime := systemEndTime.Sub(systemStartTime)

	reportDone(completedCommands, systemRunTime)
}

type Results struct {
	Commands CommandList       `json:"commands"`
	Info     map[string]string `json:"info"`
}

func reportDone(completedCommands CommandList, systemRunTime time.Duration) {

	var res = Results{}
	res.Info = make(map[string]string)

	res.Commands = completedCommands
	res.Info["systemRunTime"] = systemRunTime.String()

	results, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		slog.Error("error marshaling results")
	}

	fmt.Println(string(results))

	if flagErrors {
		for _, c := range res.Commands {
			if c.ReturnCode != 0 {
				// TODO better format?
				fmt.Fprintf(os.Stderr, "command %v exited with error code %v\n", c.Substituted, c.ReturnCode)
			}
		}
	}

}

func execute(ctx context.Context, c *Command) error {

	// TODO deal with breaking this into the command to run and its arguments
	f := strings.Fields(c.Substituted)
	name, args := f[0], f[1:]

	//fmt.Println("executing", name, args, len(args))
	slog.Debug("in execute() with", "name", name, "args", args, "arglen", len(args))

	cmd := exec.CommandContext(ctx, name, args...)

	// TODO test stderr, make sure this works ok.

	var outb, errb strings.Builder
	cmd.Stdout = &outb
	cmd.Stderr = &errb

	err := cmd.Run()
	c.ReturnCode = 0
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			c.ReturnCode = exitError.ExitCode()
			return err
		}
	}

	c.Stdout = strings.Split(outb.String(), "\n")
	c.Stderr = strings.Split(errb.String(), "\n")
	return nil
}

func start_command_loop(ctx context.Context, cmdList CommandList, flags Flags) CommandList {

	var tokens = make(chan struct{}, flags.ConcurrentLimit) // permission to run
	var done = make(chan *Command)                          // where a command goes when it's done
	var completedCommands CommandList                       // count all the done processes

	// launch each command
	for _, c := range cmdList {

		go func() {
			tokens <- struct{}{} // ge!jt permission to start
			c.StartTime = time.Now()

			//fmt.Println("running command", c.Arg)
			slog.Debug("running command", "arg", c.Substituted)

			// test: sleep for 0.1-2.6 sec
			err := execute(ctx, c)
			if err != nil {
				//fmt.Fprintf(os.Stderr, "error running command: %v %v\n", c.Arg, err)
			}
			c.EndTime = time.Now()
			rt := c.EndTime.Sub(c.StartTime)
			a, err := time.ParseDuration(rt.String())
			if err != nil {
				panic(err) // TODO
			}
			c.RunTime = a.String()

			done <- c // report status.
			<-tokens  // return token when done.
		}()

	}

	// TODO should break this loop out into another function I guess.
	for {
		select {
		case c := <-done:
			if flags.Any || (flags.FirstZero && c.ReturnCode == 0) {
				slog.Debug(fmt.Sprintf("returning %s", c.Arg))
				return CommandList{c}
			} else {
				completedCommands = append(completedCommands, c)
			}
			if len(completedCommands) == len(cmdList) {
				//fmt.Println("ALL", len(cmdList), "COMMANDS DONE")
				return completedCommands
			}
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "context popped, %v jobs done", len(completedCommands))
			return completedCommands
		}
	}
}

func PopulateFlags(cmd *cobra.Command) Flags {
	flags := Flags{}
	// I sure wish there was a cleaner way to do this
	flags.Any, _ = cmd.Flags().GetBool("any")
	flags.ConcurrentLimit, _ = cmd.Flags().GetInt("concurrent")

	tmp, _ := cmd.Flags().GetInt64("timeout")
	flags.Timeout = time.Duration(tmp) * time.Second
	flags.Token, _ = cmd.Flags().GetString("token")
	flags.FlagErrors, _ = cmd.Flags().GetBool("flag-errors")
	flags.FirstZero, _ = cmd.Flags().GetBool("first-zero")
	return flags
}

func buildListOfCommands(command string, hosts []string, token string) (CommandList, error) {
	// TODO I don't need a full template engine but should probably have something cooler than this.

	var ret CommandList
	for _, host := range hosts {
		x := Command{}
		x.Original = command
		x.Arg = host
		x.Substituted = strings.ReplaceAll(command, token, host)

		ret = append(ret, &x)
	}

	// mix them up just so there's no ordering depedency if they all take about the same time. otherwise the first one in the list
	//   tends to be the one we return first with --any.
	rand.Shuffle(len(ret), func(i, j int) {
		ret[i], ret[j] = ret[j], ret[i]
	})

	return ret, nil
}
