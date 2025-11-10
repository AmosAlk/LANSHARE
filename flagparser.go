package main

import (
	"fmt"
	"os"
)

// I prefer a unix style flag parser, where multiple single-letter flags can be combined
// in any order. For example, -ls or -sl both mean listen and send. Simple implementation here:
func ParseFlags(args []string) (listen bool, send bool) {
	for _, arg := range args {
		if len(arg) > 1 && arg[0] == '-' {
			// Expand combined flags like -ls or -sl
			for _, ch := range arg[1:] {
				switch ch {
				case 'l':
					listen = true
				case 's':
					send = true
				default:
					fmt.Printf("Unknown flag: -%c\n", ch)
					os.Exit(1)
				}
			}
		}
	}
	return
}
