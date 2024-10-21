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
	billingSession "github.com/stripe/stripe-go/v79/billingportal/session"
	"github.com/stripe/stripe-go/v79/checkout/session"
	"github.com/stripe/stripe-go/v79/customer"
	"github.com/stripe/stripe-go/v79/webhook"
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
		e.Router.POST("/customer-portal", handleCustomerPortal)
		return nil
	})

	// webhook
	app.OnBeforeServe().Add(func(e *core.ServeEvent) error {
		e.Router.POST("/webhook", func(c echo.Context) error {
			// read the request from stripe
			request := c.Request()
			payload, err := io.ReadAll(request.Body)
			if err != nil {
				return err
			}
			// unmarshall the payload into this event object
			var event stripe.Event

			webhookSecret := os.Getenv("STRIPE_WHSEC")
			if webhookSecret != "" {
				event, err = webhook.ConstructEvent(payload, request.Header.Get("Stripe-Signature"), webhookSecret)
				if err != nil {
					return err
				}
			} else {
				err = json.Unmarshal(payload, &event)
				if err != nil {
					return err
				}
			}

			switch event.Type {
			case "invoice.paid":
				var invoice stripe.Invoice
				err = json.Unmarshal(event.Data.Raw, &invoice)
				if err != nil {
					return err
				}

				err := handleInvoicePaid(&invoice, app)
				if err != nil {
					return err
				}
				fmt.Println("Set is_subscribed to true")

			case "invoice.payment_failed":
				var invoice stripe.Invoice
				err = json.Unmarshal(event.Data.Raw, &invoice)
				if err != nil {
					return err
				}

				err := handleInvoicePaymentFailed(&invoice, app)
				if err != nil {
					return err
				}

			case "customer.subscription.deleted":
				var subscription stripe.Subscription
				err = json.Unmarshal(event.Data.Raw, &subscription)
				if err != nil {
					return err
				}

				err := handleSubscriptionDeleted(&subscription, app)
				if err != nil {
					return err
				}

			default:
				fmt.Printf("Unhandled event type: %v", event.Type)
				return c.String(http.StatusOK, "Unhandled event type")
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

		// update and then save the record
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
	//return c.Redirect(http.StatusOK, checkoutSession.URL);
}

func handleInvoicePaid(invoice *stripe.Invoice, app *pocketbase.PocketBase) error {
	// check if invoice is associated with a subscription
	if invoice.Subscription == nil {
		return nil
	}

	customerId := invoice.Customer.ID

	record, err := app.Dao().FindFirstRecordByData("member", "stripe_customer_id", customerId)
	if err != nil {
		return fmt.Errorf("error finding member record %w", err)
	}

	// update and save record
	record.Set("is_subscribed", true)
	if err := app.Dao().SaveRecord(record); err != nil {
		return err
	}

	return nil
}

func handleInvoicePaymentFailed(invoice *stripe.Invoice, app *pocketbase.PocketBase) error {
	// check if invoice is associated with a subscription
	if invoice.Subscription == nil {
		return nil
	}

	customerId := invoice.Customer.ID

	record, err := app.Dao().FindFirstRecordByData("member", "stripe_customer_id", customerId)
	if err != nil {
		return fmt.Errorf("error finding member record %w", err)
	}

	// update and save record
	record.Set("is_subscribed", false)
	if err := app.Dao().SaveRecord(record); err != nil {
		return err
	}

	return nil
}

func handleSubscriptionDeleted(sub *stripe.Subscription, app *pocketbase.PocketBase) error {
	customerId := sub.Customer.ID

	record, err := app.Dao().FindFirstRecordByData("member", "stripe_customer_id", customerId)
	if err != nil {
		return fmt.Errorf("error finding member record %w", err)
	}

	// update and save record
	record.Set("is_subscribed", false)
	if err := app.Dao().SaveRecord(record); err != nil {
		return err
	}

	return nil
}

func handleCustomerPortal(c echo.Context) error {
	var requestBody struct {
		CustomerId string `json:"customerId"`
	}

	if err := c.Bind(&requestBody); err != nil {
		return c.String(http.StatusBadRequest, "Invalid customer ID")
	}

	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")

	customerPortalSession := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(requestBody.CustomerId),
		ReturnURL: stripe.String(os.Getenv("STRIPE_CUSTOMER_PORTAL_RETURN_URL")),
	}

	result, err := billingSession.New(customerPortalSession)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return c.Redirect(http.StatusOK, result.URL)
}
