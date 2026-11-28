package integrations

// Stripe's upstream tool formats every resource before returning it: the SDK
// object is projected to a small fixed field set, and list calls report
// {"has_more":bool, "<resource>":[...]}. The raw Stripe object never reaches
// the model, so each action below names its formatter explicitly.
func stripeShapes() map[string]shapeFunc {
	return map[string]shapeFunc{
		"stripe_create_customer":           stripeCustomerShape,
		"stripe_get_customer":              stripeCustomerShape,
		"stripe_update_customer":           stripeCustomerShape,
		"stripe_list_customers":            stripeListShape("customers", stripeCustomer),
		"stripe_get_customer_by_email":     stripeCustomerByEmailShape,
		"stripe_get_subscription":          stripeSubscriptionShape,
		"stripe_list_subscriptions":        stripeListShape("subscriptions", stripeSubscription),
		"stripe_create_subscription":       stripeSubscriptionShape,
		"stripe_update_subscription":       stripeSubscriptionShape,
		"stripe_create_payment_intent":     stripePaymentIntentShape,
		"stripe_get_payment_intent":        stripePaymentIntentShape,
		"stripe_confirm_payment_intent":    stripePaymentIntentShape,
		"stripe_cancel_payment_intent":     stripePaymentIntentShape,
		"stripe_list_payment_intents":      stripeListShape("payment_intents", stripePaymentIntent),
		"stripe_list_charges":              stripeListShape("charges", stripeCharge),
		"stripe_get_charge":                stripeChargeShape,
		"stripe_capture_charge":            stripeChargeShape,
		"stripe_create_refund":             stripeRefundShape,
		"stripe_get_refund":                stripeRefundShape,
		"stripe_list_refunds":              stripeListShape("refunds", stripeRefund),
		"stripe_list_invoices":             stripeListShape("invoices", stripeInvoice),
		"stripe_get_invoice":               stripeInvoiceShape,
		"stripe_create_invoice":            stripeInvoiceShape,
		"stripe_finalize_invoice":          stripeInvoiceShape,
		"stripe_pay_invoice":               stripeInvoiceShape,
		"stripe_void_invoice":              stripeInvoiceShape,
		"stripe_create_invoice_item":       stripeInvoiceItemShape,
		"stripe_list_invoice_items":        stripeListShape("invoice_items", stripeInvoiceItem),
		"stripe_delete_invoice_item":       stripeDeletedShape,
		"stripe_create_product":            stripeProductShape,
		"stripe_get_product":               stripeProductShape,
		"stripe_list_products":             stripeListShape("products", stripeProduct),
		"stripe_update_product":            stripeProductShape,
		"stripe_create_price":              stripePriceShape,
		"stripe_get_price":                 stripePriceShape,
		"stripe_list_prices":               stripeListShape("prices", stripePrice),
		"stripe_update_price":              stripePriceShape,
		"stripe_create_payment_link":       stripePaymentLinkShape,
		"stripe_get_payment_link":          stripePaymentLinkShape,
		"stripe_list_payment_links":        stripeListShape("payment_links", stripePaymentLink),
		"stripe_create_coupon":             stripeCouponShape,
		"stripe_list_coupons":              stripeListShape("coupons", stripeCoupon),
		"stripe_delete_coupon":             stripeDeletedShape,
		"stripe_get_balance":               stripeBalanceShape,
		"stripe_list_balance_transactions": stripeListShape("transactions", stripeBalanceTransaction),
		"stripe_list_webhook_endpoints":    stripeListShape("webhook_endpoints", stripeWebhookEndpoint),
		"stripe_list_payment_methods":      stripeListShape("payment_methods", stripePaymentMethod),
		"stripe_get_payment_method":        stripePaymentMethodShape,
		"stripe_detach_payment_method":     stripePaymentMethodShape,
		"stripe_list_disputes":             stripeListShape("disputes", stripeDispute),
		"stripe_list_events":               stripeListShape("events", stripeEvent),
		"stripe_create_checkout_session":   stripeCheckoutSessionShape,
	}
}

// stripeListShape mirrors `{"has_more": result.has_more, key: [format(item)]}`.
func stripeListShape(key string, format func(any) map[string]any) shapeFunc {
	return func(parsed any, _ map[string]any) any {
		object := shapeMap(parsed)
		items := shapeList(object, "data")
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, format(item))
		}
		return map[string]any{"has_more": shapeBool(object, "has_more", false), key: out}
	}
}

func stripeCustomerShape(parsed any, _ map[string]any) any { return stripeCustomer(parsed) }

func stripeCustomer(value any) map[string]any {
	c := shapeMap(value)
	return map[string]any{
		"id":          shapeText(c["id"]),
		"email":       shapeText(c["email"]),
		"name":        shapeText(c["name"]),
		"phone":       shapeText(c["phone"]),
		"description": shapeText(c["description"]),
		"created":     c["created"],
		"currency":    shapeText(c["currency"]),
		"delinquent":  c["delinquent"],
		"metadata":    shapeDict(c, "metadata"),
	}
}

// stripeCustomerByEmailShape mirrors get_customer_by_email: the first match or
// {"error":"No customer found with email: <email>"}.
func stripeCustomerByEmailShape(parsed any, input map[string]any) any {
	items := shapeList(shapeMap(parsed), "data")
	if len(items) == 0 {
		return map[string]any{"error": "No customer found with email: " + shapeString(input["email"])}
	}
	return stripeCustomer(items[0])
}

func stripeSubscriptionShape(parsed any, _ map[string]any) any { return stripeSubscription(parsed) }

func stripeSubscription(value any) map[string]any {
	s := shapeMap(value)
	items := shapeList(shapeDict(s, "items"), "data")
	outItems := make([]any, 0, len(items))
	for _, item := range items {
		entry := shapeMap(item)
		outItems = append(outItems, map[string]any{
			"id":       shapeText(entry["id"]),
			"price_id": shapeText(shapeAt(entry, "price.id")),
			"quantity": entry["quantity"],
		})
	}
	return map[string]any{
		"id":                   shapeText(s["id"]),
		"customer":             shapeText(s["customer"]),
		"status":               shapeText(s["status"]),
		"current_period_start": s["current_period_start"],
		"current_period_end":   s["current_period_end"],
		"cancel_at_period_end": s["cancel_at_period_end"],
		"canceled_at":          s["canceled_at"],
		"trial_end":            s["trial_end"],
		"created":              s["created"],
		"items":                outItems,
		"metadata":             shapeDict(s, "metadata"),
	}
}

func stripePaymentIntentShape(parsed any, _ map[string]any) any { return stripePaymentIntent(parsed) }

func stripePaymentIntent(value any) map[string]any {
	pi := shapeMap(value)
	return map[string]any{
		"id":              shapeText(pi["id"]),
		"amount":          pi["amount"],
		"amount_received": pi["amount_received"],
		"currency":        shapeText(pi["currency"]),
		"status":          shapeText(pi["status"]),
		"customer":        shapeText(pi["customer"]),
		"description":     shapeText(pi["description"]),
		"receipt_email":   shapeText(pi["receipt_email"]),
		"payment_method":  shapeText(pi["payment_method"]),
		"created":         pi["created"],
		"metadata":        shapeDict(pi, "metadata"),
	}
}

func stripeChargeShape(parsed any, _ map[string]any) any { return stripeCharge(parsed) }

func stripeCharge(value any) map[string]any {
	c := shapeMap(value)
	return map[string]any{
		"id":              shapeText(c["id"]),
		"amount":          c["amount"],
		"amount_captured": c["amount_captured"],
		"amount_refunded": c["amount_refunded"],
		"currency":        shapeText(c["currency"]),
		"status":          shapeText(c["status"]),
		"paid":            c["paid"],
		"refunded":        c["refunded"],
		"customer":        shapeText(c["customer"]),
		"description":     shapeText(c["description"]),
		"receipt_email":   shapeText(c["receipt_email"]),
		"receipt_url":     shapeText(c["receipt_url"]),
		"payment_intent":  shapeText(c["payment_intent"]),
		"created":         c["created"],
		"metadata":        shapeDict(c, "metadata"),
	}
}

func stripeRefundShape(parsed any, _ map[string]any) any { return stripeRefund(parsed) }

func stripeRefund(value any) map[string]any {
	r := shapeMap(value)
	return map[string]any{
		"id":             shapeText(r["id"]),
		"amount":         r["amount"],
		"currency":       shapeText(r["currency"]),
		"status":         shapeText(r["status"]),
		"charge":         shapeText(r["charge"]),
		"payment_intent": shapeText(r["payment_intent"]),
		"reason":         shapeText(r["reason"]),
		"created":        r["created"],
		"metadata":       shapeDict(r, "metadata"),
	}
}

func stripeInvoiceShape(parsed any, _ map[string]any) any { return stripeInvoice(parsed) }

func stripeInvoice(value any) map[string]any {
	inv := shapeMap(value)
	return map[string]any{
		"id":                 shapeText(inv["id"]),
		"customer":           shapeText(inv["customer"]),
		"subscription":       shapeText(inv["subscription"]),
		"status":             shapeText(inv["status"]),
		"amount_due":         inv["amount_due"],
		"amount_paid":        inv["amount_paid"],
		"amount_remaining":   inv["amount_remaining"],
		"currency":           shapeText(inv["currency"]),
		"description":        shapeText(inv["description"]),
		"hosted_invoice_url": shapeText(inv["hosted_invoice_url"]),
		"invoice_pdf":        shapeText(inv["invoice_pdf"]),
		"due_date":           inv["due_date"],
		"created":            inv["created"],
		"period_start":       inv["period_start"],
		"period_end":         inv["period_end"],
		"metadata":           shapeDict(inv, "metadata"),
	}
}

func stripeInvoiceItemShape(parsed any, _ map[string]any) any { return stripeInvoiceItem(parsed) }

func stripeInvoiceItem(value any) map[string]any {
	item := shapeMap(value)
	return map[string]any{
		"id":          shapeText(item["id"]),
		"customer":    shapeText(item["customer"]),
		"invoice":     shapeText(item["invoice"]),
		"amount":      item["amount"],
		"currency":    shapeText(item["currency"]),
		"description": shapeText(item["description"]),
		"quantity":    item["quantity"],
		"created":     item["created"],
		"metadata":    shapeDict(item, "metadata"),
	}
}

// stripeDeletedShape mirrors the invoice-item and coupon delete responses:
// {"id":...,"deleted":true}.
func stripeDeletedShape(parsed any, _ map[string]any) any {
	object := shapeMap(parsed)
	return map[string]any{"id": shapeText(object["id"]), "deleted": shapeBool(object, "deleted", false)}
}

func stripeProductShape(parsed any, _ map[string]any) any { return stripeProduct(parsed) }

func stripeProduct(value any) map[string]any {
	p := shapeMap(value)
	return map[string]any{
		"id":          shapeText(p["id"]),
		"name":        shapeText(p["name"]),
		"description": shapeText(p["description"]),
		"active":      p["active"],
		"created":     p["created"],
		"updated":     p["updated"],
		"metadata":    shapeDict(p, "metadata"),
	}
}

func stripePriceShape(parsed any, _ map[string]any) any { return stripePrice(parsed) }

func stripePrice(value any) map[string]any {
	p := shapeMap(value)
	var recurring any
	if raw, ok := p["recurring"]; ok && raw != nil {
		cycle := shapeMap(raw)
		recurring = map[string]any{
			"interval":       shapeText(cycle["interval"]),
			"interval_count": cycle["interval_count"],
		}
	}
	return map[string]any{
		"id":          shapeText(p["id"]),
		"product":     shapeText(p["product"]),
		"currency":    shapeText(p["currency"]),
		"unit_amount": p["unit_amount"],
		"nickname":    shapeText(p["nickname"]),
		"active":      p["active"],
		"type":        shapeText(p["type"]),
		"recurring":   recurring,
		"created":     p["created"],
		"metadata":    shapeDict(p, "metadata"),
	}
}

func stripePaymentLinkShape(parsed any, _ map[string]any) any { return stripePaymentLink(parsed) }

func stripePaymentLink(value any) map[string]any {
	link := shapeMap(value)
	items := shapeList(shapeDict(link, "line_items"), "data")
	outItems := make([]any, 0, len(items))
	for _, item := range items {
		entry := shapeMap(item)
		var price any
		if raw, ok := entry["price"]; ok && raw != nil {
			price = shapeText(shapeAt(entry, "price.id"))
		}
		outItems = append(outItems, map[string]any{
			"price":    price,
			"quantity": entry["quantity"],
		})
	}
	return map[string]any{
		"id":         shapeText(link["id"]),
		"url":        shapeText(link["url"]),
		"active":     link["active"],
		"currency":   shapeText(link["currency"]),
		"line_items": outItems,
		"created":    link["created"],
		"metadata":   shapeDict(link, "metadata"),
	}
}

func stripeCouponShape(parsed any, _ map[string]any) any { return stripeCoupon(parsed) }

func stripeCoupon(value any) map[string]any {
	c := shapeMap(value)
	return map[string]any{
		"id":                 shapeText(c["id"]),
		"name":               shapeText(c["name"]),
		"percent_off":        c["percent_off"],
		"amount_off":         c["amount_off"],
		"currency":           shapeText(c["currency"]),
		"duration":           shapeText(c["duration"]),
		"duration_in_months": c["duration_in_months"],
		"max_redemptions":    c["max_redemptions"],
		"times_redeemed":     c["times_redeemed"],
		"valid":              c["valid"],
		"created":            c["created"],
		"metadata":           shapeDict(c, "metadata"),
	}
}

// stripeBalanceShape mirrors get_balance (no has_more).
func stripeBalanceShape(parsed any, _ map[string]any) any {
	balance := shapeMap(parsed)
	project := func(values []any) []any {
		out := make([]any, 0, len(values))
		for _, value := range values {
			entry := shapeMap(value)
			out = append(out, map[string]any{
				"amount":   entry["amount"],
				"currency": shapeText(entry["currency"]),
			})
		}
		return out
	}
	return map[string]any{
		"available": project(shapeList(balance, "available")),
		"pending":   project(shapeList(balance, "pending")),
	}
}

func stripeBalanceTransaction(value any) map[string]any {
	t := shapeMap(value)
	return map[string]any{
		"id":          shapeText(t["id"]),
		"amount":      t["amount"],
		"currency":    shapeText(t["currency"]),
		"net":         t["net"],
		"fee":         t["fee"],
		"type":        shapeText(t["type"]),
		"status":      shapeText(t["status"]),
		"description": shapeText(t["description"]),
		"created":     t["created"],
	}
}

func stripeWebhookEndpoint(value any) map[string]any {
	we := shapeMap(value)
	return map[string]any{
		"id":             shapeText(we["id"]),
		"url":            shapeText(we["url"]),
		"status":         shapeText(we["status"]),
		"enabled_events": shapeList(we, "enabled_events"),
		"created":        we["created"],
	}
}

func stripePaymentMethodShape(parsed any, _ map[string]any) any { return stripePaymentMethod(parsed) }

func stripePaymentMethod(value any) map[string]any {
	pm := shapeMap(value)
	var card any
	if raw, ok := pm["card"]; ok && raw != nil {
		details := shapeMap(raw)
		card = map[string]any{
			"brand":     shapeText(details["brand"]),
			"last4":     shapeText(details["last4"]),
			"exp_month": details["exp_month"],
			"exp_year":  details["exp_year"],
			"country":   shapeText(details["country"]),
		}
	}
	return map[string]any{
		"id":       shapeText(pm["id"]),
		"type":     shapeText(pm["type"]),
		"customer": shapeText(pm["customer"]),
		"card":     card,
		"created":  pm["created"],
		"metadata": shapeDict(pm, "metadata"),
	}
}

func stripeDispute(value any) map[string]any {
	d := shapeMap(value)
	return map[string]any{
		"id":              shapeText(d["id"]),
		"amount":          d["amount"],
		"currency":        shapeText(d["currency"]),
		"charge":          shapeText(d["charge"]),
		"payment_intent":  shapeText(d["payment_intent"]),
		"reason":          shapeText(d["reason"]),
		"status":          shapeText(d["status"]),
		"created":         d["created"],
		"evidence_due_by": shapeText(shapeAt(d, "evidence_details.due_by")),
	}
}

func stripeEvent(value any) map[string]any {
	e := shapeMap(value)
	return map[string]any{
		"id":        shapeText(e["id"]),
		"type":      shapeText(e["type"]),
		"created":   e["created"],
		"object_id": shapeText(shapeAt(e, "data.object.id")),
	}
}

// stripeCheckoutSessionShape mirrors create_checkout_session.
func stripeCheckoutSessionShape(parsed any, _ map[string]any) any {
	session := shapeMap(parsed)
	return map[string]any{
		"id":             shapeText(session["id"]),
		"url":            shapeText(session["url"]),
		"mode":           shapeText(session["mode"]),
		"status":         shapeText(session["status"]),
		"payment_status": shapeText(session["payment_status"]),
		"customer":       shapeText(session["customer"]),
		"amount_total":   session["amount_total"],
		"currency":       shapeText(session["currency"]),
		"created":        session["created"],
	}
}
