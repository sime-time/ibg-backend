package main

import (
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v5"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/customer"
)

func main() {
	app := pocketbase.New()

	// stripe integration
	err := godotenv.Load(".env")
	if err != nil {
		log.Fatal("Failed to load .env file")
	}
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")

	// route: get publishable-key
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		e.Router.GET("/publishable-key", handlePublishableKey)
		return nil
	})

	// when new member is created, create a new stripe customer
	app.OnRecordAfterCreateRequest("member").Add(func(e *core.RecordCreateEvent) error {
		email := e.Record.Get("email").(string)
		name := e.Record.Get("name").(string)

		// create a new stripe customer
		newCustomer, err := customer.New(&stripe.CustomerParams{
			Email: stripe.String(email),
			Name:  stripe.String(name),
		})
		if err != nil {
			return err
		}

		// store the stripe customer id in pocketbase record
		e.Record.Set("stripe_customer_id", newCustomer.ID)

		// save the updated record
		if err := app.Dao().SaveRecord(e.Record); err != nil {
			return err
		}

		return nil
	})

	// start pocketbase server
	app_err := app.Start()
	if app_err != nil {
		log.Fatal(app_err)
	}
}

type PublishKeyParams struct {
	StripeKey string `json:"key"`
}

func handlePublishableKey(c echo.Context) (err error) {
	publish_key := PublishKeyParams{
		StripeKey: os.Getenv("STRIPE_PUBLISHABLE_KEY"),
	}
	return c.JSON(http.StatusOK, publish_key)
}
