package cmd

import (
	"fmt"
)

func Sessions(args []string) {
	fmt.Println("Sessions are no longer managed by dispatch.")
	fmt.Println("Pi processes are ephemeral — they start, do work, and exit.")
	fmt.Println()
	fmt.Println("To see active Pi processes:")
	fmt.Println("  ps aux | grep pi")
	fmt.Println()
	fmt.Println("To see logs:")
	fmt.Println("  ls logs/pi-*.log")
}
