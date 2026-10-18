// Command hippod reads the GNSS module's hippo protocol and writes NMEA.
package main

import (
	"os"

	hippo "github.com/W-Floyd/go-hippod"
)

func main() {
	os.Exit(hippo.Main(os.Args[1:], os.Stdout, os.Stderr))
}
