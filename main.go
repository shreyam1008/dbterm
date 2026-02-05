package main

import (
	"fmt"

	"github.com/nabsk911/pgterm/ui"
)

func main() {
	app := ui.NewApp()

	if err := app.Run(); err != nil {
		fmt.Printf("Error running the app: %s\n", err)
	}
}
