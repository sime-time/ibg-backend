package main

import (
	"encoding/json"
	"fmt"
	"io"
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

	// webhook
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		e.Router.POST("/webhook", func(c echo.Context) error {
			const MaxBodyBytes = int64(65536)
			body := io.LimitReader(c.Request().Body, MaxBodyBytes)

			payload, err := io.ReadAll(body)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading request body: %v\n", err)
				return echo.NewHTTPError(http.StatusServiceUnavailable, "Error reading request body")
			}

			event := stripe.Event{}
			if err := json.Unmarshal(payload, &event); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to parse webhook body json %v\n", err.Error())
				return echo.NewHTTPError(http.StatusBadRequest, "Failed to parse webhook body")
			}

			fmt.Println(payload)

			// stripe signature verification
			/*
				whsec := os.Getenv("STRIPE_WHSEC")
				event, err := webhook.ConstructEvent(payload, signatureHeader, whsec)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Signature verification failed %v\n", err.Error())
					return echo.NewHTTPError(http.StatusBadRequest, "Signature verification failed")
				}*/

			switch event.Type {
			case "invoice.paid":
				var invoice stripe.Invoice
				err := json.Unmarshal(event.Data.Raw, &invoice)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error parsing invoice JSON: %v\n", err)
					return echo.NewHTTPError(http.StatusBadRequest, "Error parsing invoice JSON")
				}
				if err := handleInvoicePaid(invoice, app); err != nil {
					fmt.Fprintf(os.Stderr, "Error handling invoice paid: %v\n", err)
					return echo.NewHTTPError(http.StatusInternalServerError, "Error processing invoice")
				}

			case "invoice.payment_failed":
				var invoice stripe.Invoice
				err := json.Unmarshal(event.Data.Raw, &invoice)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error parsing invoice JSON: %v\n", err)
					return echo.NewHTTPError(http.StatusBadRequest, "Error parsing invoice JSON")
				}
				if err := handleInvoicePaymentFailed(invoice, app); err != nil {
					fmt.Fprintf(os.Stderr, "Error handling invoice failed: %v\n", err)
					return echo.NewHTTPError(http.StatusInternalServerError, "Error processing invoice payment fail")
				}

			case "customer.subscription.deleted":
				var subscription stripe.Subscription
				err := json.Unmarshal(event.Data.Raw, &subscription)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error parsing subscription JSON: %v\n", err)
					return echo.NewHTTPError(http.StatusBadRequest, "Error parsing subscription JSON")
				}
				if err := handleSubscriptionDeleted(subscription, app); err != nil {
					fmt.Fprintf(os.Stderr, "Error handling subscription deletion: %v\n", err)
					return echo.NewHTTPError(http.StatusInternalServerError, "Error processing subscription deletion")
				}

			default:
				fmt.Fprintf(os.Stderr, "Unhandled event type %s\n", event.Type)
			}

			return c.NoContent(http.StatusOK)
		})

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

func handleInvoicePaid(invoice stripe.Invoice, app *pocketbase.PocketBase) error {
	// check if invoice is associated with a subscription
	if invoice.Subscription == nil {
		return nil
	}

	customerID := invoice.Customer.ID

	record, err := app.Dao().FindFirstRecordByData("member", "stripe_customer_id", customerID)
	if err != nil {
		return fmt.Errorf("error finding member record %w", err)
	}

	record.Set("is_subscribed", true)

	return nil
}

func handleInvoicePaymentFailed(invoice stripe.Invoice, app *pocketbase.PocketBase) error {
	// check if invoice is associated with a subscription
	if invoice.Subscription == nil {
		return nil
	}

	customerID := invoice.Customer.ID

	record, err := app.Dao().FindFirstRecordByData("member", "stripe_customer_id", customerID)
	if err != nil {
		return fmt.Errorf("error finding member record %w", err)
	}

	record.Set("is_subscribed", false)

	return nil
}

func handleSubscriptionDeleted(sub stripe.Subscription, app *pocketbase.PocketBase) error {
	customerID := sub.Customer.ID

	record, err := app.Dao().FindFirstRecordByData("member", "stripe_customer_id", customerID)
	if err != nil {
		return fmt.Errorf("error finding member record %w", err)
	}

	record.Set("is_subscribed", false)

	return nil
}
