package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/labstack/echo/v5"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/stripe/stripe-go/v79"
	billingSession "github.com/stripe/stripe-go/v79/billingportal/session"
	"github.com/stripe/stripe-go/v79/checkout/session"
	"github.com/stripe/stripe-go/v79/customer"
	"github.com/stripe/stripe-go/v79/customersession"
	"github.com/stripe/stripe-go/v79/paymentintent"
	"github.com/stripe/stripe-go/v79/product"
	"github.com/stripe/stripe-go/v79/subscription"
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
		e.Router.POST("/cancel-subscription", handleCancelSubscription)
		e.Router.POST("/client-secret", handleClientSecret)
		e.Router.POST("/revenue-data", handleRevenueData)
		return nil
	})

	// webhooks
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

	var productName string
	if len(invoice.Lines.Data) > 0 && invoice.Lines.Data[0].Price != nil {
		productId := invoice.Lines.Data[0].Price.Product.ID
		params := &stripe.ProductParams{}
		result, err := product.Get(productId, params)
		if err != nil {
			return fmt.Errorf("error finding stripe product %w", err)
		}
		productName = result.Name
	}

	record, err := app.Dao().FindFirstRecordByData("member", "stripe_customer_id", customerId)
	if err != nil {
		return fmt.Errorf("error finding member record %w", err)
	}

	// update and save record
	record.Set("is_subscribed", true)
	record.Set("program", productName)
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
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "Invalid customer ID",
		})
	}
	customerPortalSession := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(requestBody.CustomerId),
		ReturnURL: stripe.String(os.Getenv("STRIPE_CUSTOMER_PORTAL_RETURN_URL")),
	}
	result, err := billingSession.New(customerPortalSession)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error creating customer portal": err.Error()})
	}
	return c.JSON(http.StatusOK, result)
}

func handleCancelSubscription(c echo.Context) error {
	var requestBody struct {
		CustomerId string `json:"customerId"`
	}
	if err := c.Bind(&requestBody); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "Invalid customer ID",
		})
	}
	// get list of all subscriptions with the same customer id
	params := &stripe.SubscriptionListParams{
		Customer: &requestBody.CustomerId,
	}
	subList := subscription.List(params)
	// cancel the first subscription found for this customer
	for subList.Next() {
		sub := subList.Subscription()
		_, err := subscription.Cancel(sub.ID, nil)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, echo.Map{
				"error": "Failed to cancel subscription",
			})
		} else {
			fmt.Println("Member Subscription Cancelled")
			return c.JSON(http.StatusOK, echo.Map{
				"message": "Subscription cancelled successfully",
			})
		}
	}
	return c.JSON(http.StatusNotFound, echo.Map{
		"error": "No subscription found for the customer",
	})
}

func handleClientSecret(c echo.Context) error {
	// parse the request json
	var requestBody struct {
		CustomerId string `json:"customerId"`
	}
	if err := c.Bind(&requestBody); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "Invalid request body",
		})
	}
	// create customer-specific checkout
	params := &stripe.CustomerSessionParams{
		Customer: stripe.String(requestBody.CustomerId),
		Components: &stripe.CustomerSessionComponentsParams{
			PricingTable: &stripe.CustomerSessionComponentsPricingTableParams{
				Enabled: stripe.Bool(true),
			},
		},
	}
	result, err := customersession.New(params)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, echo.Map{
			"error": "Internal server error",
		})
	}
	// return the client secret to personalize the checkout to the customer
	return c.JSON(http.StatusOK, map[string]string{"client_secret": result.ClientSecret})
}

type Timeframe struct {
	start int64
	end   int64
}

// to set when to start and stop the collection of revenue data
func getTimeframe(monthsAgo int) Timeframe {
	// current time
	endTime := time.Now()
	fmt.Println("End time: ")
	fmt.Println(endTime.Format("2006-01-02"))

	// make startTime the first day of the month
	startTime := endTime.AddDate(0, -monthsAgo, 1-endTime.Day())
	fmt.Println("Start time: ")
	fmt.Println(startTime.Format("2006-01-02"))

	// convert to integers
	startTimestamp := startTime.Unix()
	endTimestamp := endTime.Unix()

	timeframe := Timeframe{
		start: startTimestamp,
		end:   endTimestamp,
	}
	return timeframe
}

func handleRevenueData(c echo.Context) error {
	var requestBody struct {
		MonthsAgo int `json:"monthsAgo"`
	}
	if err := c.Bind(&requestBody); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{
			"error": "Invalid request body",
		})
	}
	timeframe := getTimeframe(requestBody.MonthsAgo)

	// filter the list of payments to match the timeframe
	params := &stripe.PaymentIntentListParams{}
	params.Filters.AddFilter("created", "gte", strconv.FormatInt(timeframe.start, 10))
	params.Filters.AddFilter("created", "lte", strconv.FormatInt(timeframe.end, 10))
	paymentList := paymentintent.List(params)

	// map of payment amounts for each month
	revenueData := make(map[string]map[string]int64)

	for paymentList.Next() {
		payment := paymentList.PaymentIntent()

		// get the month and year of payment
		t := time.Unix(payment.Created, 0)
		month := t.Format("January")
		year := t.Format("2006")

		if payment.Status == "succeeded" {
			// First, check if the year exists and initialize if needed
			if _, exists := revenueData[year]; !exists {
				revenueData[year] = make(map[string]int64)
			}
			revenueData[year][month] = revenueData[year][month] + payment.Amount
		}
	}

	if err := paymentList.Err(); err != nil {
		log.Printf("Error iterating payment intents: %v\n", err)
		return c.JSON(http.StatusInternalServerError, echo.Map{
			"error": "Failed to fetch payment intents",
		})
	}

	return c.JSON(http.StatusOK, revenueData)
}
