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
	"strconv"
	"strings"

	"github.com/apex/log"
	"github.com/blacktop/go-apfs"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// ConvertStrToInt converts an input string to uint64
func ConvertStrToInt(intStr string) (uint64, error) {
	intStr = strings.ToLower(intStr)

	if strings.ContainsAny(strings.ToLower(intStr), "xabcdef") {
		intStr = strings.Replace(intStr, "0x", "", -1)
		intStr = strings.Replace(intStr, "x", "", -1)
		if out, err := strconv.ParseUint(intStr, 16, 64); err == nil {
			return out, err
		}
		log.Warn("assuming given integer is in decimal")
	}
	return strconv.ParseUint(intStr, 10, 64)
}

// oidCmd represents the oid command
var oidCmd = &cobra.Command{
	Use:   "oid",
	Short: "🚧 Show Oid Info",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {

		if Verbose {
			log.SetLevel(log.DebugLevel)
		}

		color.NoColor = !cmd.Flag("color").Changed

		fpath := filepath.Clean(args[0])

		a, err := apfs.Open(fpath)
		if err != nil {
			return err
		}
		defer a.Close()

		oid, err := ConvertStrToInt(args[1])
		if err != nil {
			return err
		}

		if err := a.OidInfo(oid); err != nil {
			return err
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(oidCmd)
	oidCmd.Flags().Bool("color", false, "Force color output")
}
