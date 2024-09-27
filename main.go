package main

import (
	"log"

	"github.com/pocketbase/pocketbase"
)

func main() {
	app := pocketbase.New()

	// stripe integration

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
