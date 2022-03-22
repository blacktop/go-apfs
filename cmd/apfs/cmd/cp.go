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
	"os"
	"path/filepath"

	"github.com/apex/log"
	"github.com/blacktop/go-apfs"
	"github.com/blacktop/go-apfs/pkg/disk/dmg"
	"github.com/spf13/cobra"
)

// cpCmd represents the cp command
var cpCmd = &cobra.Command{
	Use:   "cp <DMG> <SRC> [DST]",
	Short: "🚧 Copy file from APFS container",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {

		if Verbose {
			log.SetLevel(log.DebugLevel)
		}

		dmgPath := filepath.Clean(args[0])

		dev, err := dmg.Open(dmgPath, &dmg.Config{DisableCache: true})
		if err != nil {
			return err
		}
		defer dev.Close()

		a, err := apfs.NewAPFS(dev)
		if err != nil {
			return err
		}

		if len(args) > 2 {
			if err := a.Copy(args[1], args[2]); err != nil {
				log.Fatal(err.Error())
			}
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				log.Fatal(err.Error())
			}
			if err := a.Copy(args[1], cwd); err != nil {
				log.Fatal(err.Error())
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(cpCmd)
}
