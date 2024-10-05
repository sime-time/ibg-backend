package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v5"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/paymentintent"
	"github.com/stripe/stripe-go/v79/price"
)

type CheckoutData struct {
	ClientSecret string `json:"client_secret"`
}

func main() {
	app := pocketbase.New()

	// stripe integration
	err := godotenv.Load(".env")
	if err != nil {
		log.Fatal("Failed to load .env file")
	}
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")

	// route: hello example
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		e.Router.GET("/hello/:name", func(c echo.Context) error {
			name := c.PathParam("name")
			return c.JSON(http.StatusOK, map[string]string{"message": "Hello " + name})
		} /* optional middlewares */)

		return nil
	})

	// route: public key
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		e.Router.GET("/public-key", publicKeyHandler)
		return nil
	})

	// route: create payment intent
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		e.Router.POST("/create-payment-intent", handleCreatePaymentIntent)
		return nil
	})

	// start pocketbase server
	app_err := app.Start()
	if app_err != nil {
		log.Fatal(app_err)
	}
}

type PublicKeyParams struct {
	StripeKey string `json:"key"`
}

func publicKeyHandler(c echo.Context) (err error) {
	data := PublicKeyParams{
		StripeKey: os.Getenv("STRIPE_PUBLISHABLE_KEY"),
	}
	return c.JSON(http.StatusOK, data)
}

func handleCreatePaymentIntent(c echo.Context) (err error) {
	// struct to recieve martial art selection from client request
	var reqBody struct {
		MartialArt string `json:"martial_art"`
	}
	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "invalid request body",
		})
	}

	// key:value pair of price ids
	priceMap := map[string]string{
		"boxing":    "price_1Q30Yu06MCKUDe5TaDFKUmwn",
		"jiu-jitsu": "price_1Q30ZV06MCKUDe5T8hxKMWhK",
		"mma":       "price_1Q3M2m06MCKUDe5TCJbaFfzR",
	}

	// find price id using requested martial art
	stripePrice, err := price.Get(priceMap[reqBody.MartialArt], nil)
	if err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "failed to retrieve price",
		})
	}

	// create payment intent parameters
	piParams := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(stripePrice.UnitAmount),
		Currency: stripe.String(string(stripePrice.Currency)),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
	}
	piParams.AddMetadata("price_id", stripePrice.ID)

	// create payment intent
	pi, err := paymentintent.New(piParams)
	if err != nil {
		if stripeErr, ok := err.(*stripe.Error); ok {
			fmt.Printf("Stripe error: %v\n", stripeErr.Msg)
			return c.JSON(http.StatusInternalServerError, echo.Map{
				"error": stripeErr.Msg,
			})
		} else {
			fmt.Printf("Other error occurred: %v\n", err.Error())
			return c.JSON(http.StatusInternalServerError, echo.Map{
				"error": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, struct {
		ClientSecret string `json:"clientSecret"`
	}{
		ClientSecret: pi.ClientSecret,
	})
}
