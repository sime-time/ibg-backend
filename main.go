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
	"github.com/stripe/stripe-go/v79/checkout/session"
	"github.com/stripe/stripe-go/v79/customer"
)

func main() {
	app := pocketbase.New()

	// stripe integration
	err := godotenv.Load(".env")
	if err != nil {
		log.Print("Failed to load .env file")
	}
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")

	// routes
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		e.Router.GET("/publishable-key", handlePublishableKey)
		e.Router.POST("/checkout-session", handleCheckoutSession)
		return nil
	})

	// after creating a new member -> create a new stripe customer
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

type PublishableKeyParams struct {
	StripeKey string `json:"key"`
}

func handlePublishableKey(c echo.Context) (err error) {
	pubKey := PublishableKeyParams{
		StripeKey: os.Getenv("STRIPE_PUBLISHABLE_KEY"),
	}
	return c.JSON(http.StatusOK, pubKey)
}

func handleCheckoutSession(c echo.Context) error {
	// parse the request json
	var requestBody struct {
		CustomerId string `json:"customerId"`
		PriceId    string `json:"priceId"`
	}
	if err := c.Bind(&requestBody); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "Invalid request body",
		})
	}

	// create customer-specific checkout
	checkoutSession, err := session.New(&stripe.CheckoutSessionParams{
		Customer:   stripe.String(requestBody.CustomerId),
		Mode:       stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		SuccessURL: stripe.String(os.Getenv("STRIPE_SUCCESS_URL")),
		CancelURL:  stripe.String(os.Getenv("STRIPE_CANCEL_URL")),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(requestBody.PriceId),
				Quantity: stripe.Int64(1),
			},
		},
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error creating checkout session": err.Error()})
	}

	// return the url to the stripe checkout session
	return c.JSON(http.StatusOK, map[string]string{"url": checkoutSession.URL})
}

type DocuSealPayload struct {
	EventType string `json:"event_type"`
	Data      struct {
		Email         string `json:"email"`
		Status        string `json:"status"`
		SubmissionUrl string `json:"url"`
	} `json:"data"`
}

/*
func handleDocuSeal(c echo.Context) (err error) {
	// parse the submission payload

	// event type should be "form.completed"
	// grab the email
	// find the member with the same email
	// take the submission/audit url
	// insert the url into the member record
}
*/
