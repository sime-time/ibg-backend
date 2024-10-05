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
	"github.com/stripe/stripe-go/product"
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

// RetrievePriceByProductID fetches the first price object for a given product ID
func RetrievePriceByProductID(productID string) (*stripe.Price, error) {

	// Set the parameters to filter prices by product ID
	params := &stripe.PriceListParams{
		Product: stripe.String(productID), // Filter prices by product ID
		Limit:   stripe.Int64(1),          // Limit the result to 1 price for efficiency
	}

	// List prices for the product ID
	i := price.List(params)

	// Retrieve the first price found for the product
	if i.Next() {
		return i.Price(), nil // Return the first price object
	}

	// Handle errors or the case when no price is found
	if err := i.Err(); err != nil {
		return nil, fmt.Errorf("error retrieving price: %v", err)
	}

	return nil, fmt.Errorf("no price found for product ID: %s", productID)
}

func handleCreatePaymentIntent(c echo.Context) (err error) {
	// struct to recieve martial art selection from client request
	var reqBody struct {
		MartialArt string `json:"martial_art"`
	}
	if err := c.Bind(&reqBody); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "Invalid request body",
		})
	}

	stripeMap := map[string]string{
		"boxing":    "prod_QuqFFinwkIALdT",
		"jiu-jitsu": "prod_QuqGPXOJMMKQvF",
		"mma":       "prod_QvCQ4H6b78W79w",
	}
	product, err := product.Get(stripeMap[reqBody.MartialArt], nil)
	if err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "Invalid martial art name",
		})
	}
	price, err := RetrievePriceByProductID(product.ID)
	if err != nil {
		log.Fatalf("Failed to retrieve price: %v", err)
	}

	// create payment intent parameters
	piParams := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(price.UnitAmount),
		Currency: stripe.String(string(price.Currency)),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
	}

	// add the productID to the payment intent metadata
	piParams.AddMetadata("product_id", product.ID)
	piParams.AddMetadata("price_id", price.ID)

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
