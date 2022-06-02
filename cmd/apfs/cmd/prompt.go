/*
Copyright © 2022 blacktop

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/
package cmd

import (
	"path/filepath"
	"os"
	"fmt"
	"strings"

	"github.com/apex/log"
	"github.com/blacktop/go-apfs"
	"github.com/blacktop/go-apfs/pkg/disk/dmg"
	"github.com/spf13/cobra"
	"github.com/c-bata/go-prompt"
)

type promptContext struct {
	pwd string
	a *apfs.APFS
}

var pctx *promptContext

func completer(d prompt.Document) []prompt.Suggest {
	s := []prompt.Suggest{
		{Text: "cat",  Description: "cat(1) file"},
		{Text: "cd",   Description: "Change directory"},
		{Text: "cp",   Description: "Copy file"},
		{Text: "exit", Description: "Quit prompt"},
		{Text: "ls",   Description: "List files"},
		{Text: "pwd",  Description: "Print working directory name"},
	}
	return prompt.FilterHasPrefix(s, d.TextBeforeCursor(), true)
}

func Executor(s string) {
	s = strings.TrimSpace(s)

	if s == "" {
		return
	} else if s == "exit" {
		os.Exit(0)
		return
	}

	args := strings.Fields(s)

	var path string

	if len(args) >= 2 {
		if strings.HasPrefix(args[1], "/") {
			path = args[1]
		} else {
			path = filepath.Join(pctx.pwd, args[1])
		}
	}

	switch (args[0]) {
	case "cd":
		if len(args) == 1 {
			pctx.pwd = "/"
		} else if len(args) >= 2 {
			pctx.pwd = path
		}
	case "pwd":
		fmt.Println(pctx.pwd)
	case "ls":
		if len(args) > 1 {
			if err := pctx.a.List(path); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err.Error())
				return
			}
		} else {
			if err := pctx.a.List(pctx.pwd); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err.Error())
				return
			}
		}
	case "cat":
		if len(args) < 2 {
			return
		}
		for i := 1; i < len(args); i++ {
			if strings.HasPrefix(args[i], "/") {
				path = args[i]
			} else {
				path = filepath.Join(pctx.pwd, args[i])
			}
			if err := pctx.a.Cat(path); err != nil {
				fmt.Fprintln(os.Stderr, "Error:", err.Error())
				return
			}
		}
	case "cp":
		if len(args) < 1 {
			return
		}

		if len(args) >= 3 {
			if err := pctx.a.Copy(path, args[2]); err != nil {
				fmt.Fprintln(os.Stderr, "Error:" + err.Error())
				return
			}
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintln(os.Stderr, "Error:" + err.Error())
				return
			}
			if err := pctx.a.Copy(path, cwd); err != nil {
				fmt.Fprintln(os.Stderr, "Error:" + err.Error())
				return
			}
		}
	default:
		fmt.Fprintln(os.Stderr, "command not found: " + args[0])
	}
}

func PromptPrefix() (string, bool) {
	return pctx.pwd + " > ", true
}

// promptCmd represents the prompt command
var promptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "🚧 prompt to interactively run multiple commands",
	Args:  cobra.MinimumNArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error {
		if Verbose {
			log.SetLevel(log.DebugLevel)
		}

		dmgPath := filepath.Clean(args[0])

		dev, err := dmg.Open(dmgPath, &dmg.Config{DisableCache: true})
		if err != nil {
			return err
		}
		defer dev.Close()

		apfs, err := apfs.NewAPFS(dev)
		if err != nil {
			return err
		}

		pctx = &promptContext{
			pwd: "/",
			a: apfs,
		}

		p := prompt.New(Executor, completer, prompt.OptionLivePrefix(PromptPrefix))

		p.Run()

		return nil
	},
}

func init() {
	rootCmd.AddCommand(promptCmd)
}
