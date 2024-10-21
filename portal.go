package main

import (
	"net/http"
	"os"

	"github.com/stripe/stripe-go/v79/billingportal/session"

	"github.com/labstack/echo/v5"
	"github.com/stripe/stripe-go/v79"
)

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

	result, err := session.New(customerPortalSession)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return c.Redirect(http.StatusOK, result.URL)
}
