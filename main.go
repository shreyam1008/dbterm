package main

import (
	"fmt"

	"github.com/shreyam1008/dbterm/ui"
)

func main() {
	app := ui.NewApp()

	if err := app.Run(); err != nil {
		fmt.Printf("Error running dbterm: %s\n", err)
	}
}
