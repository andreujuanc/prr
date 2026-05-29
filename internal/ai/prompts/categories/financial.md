### FINANCIAL (category: "financial")

Code that handles money, pricing, billing, or financial calculations.

## Shapes or common patterns

**money-arithmetic** — Floating point, rounding, precision:
- Monetary amounts stored or computed as floating point (float32/float64/double)
- Multiplication or division of money values without controlled rounding
- Currency conversion with accumulated floating point error
- Rounding mode not specified (banker's rounding vs truncation vs round-half-up)
- Precision loss when converting between representations (dollars ↔ cents)
- Comparison of money values using floating point equality
- Integer overflow when working with cents/minor units at scale

**billing-logic** — Pricing, discounts, tax, proration:
- Discount applied multiple times (stacking exploits)
- Negative quantities or negative prices not rejected
- Tax calculated on discounted amount vs original (order of operations)
- Proration logic that doesn't handle edge cases (first day, last day, leap years)
- Free tier or trial logic that can be reset or bypassed
- Coupon codes without usage limits or expiry enforcement
- Price changes not locked at time of purchase (TOCTOU on pricing)

**payment-integration** — Payment processor interaction, webhooks:
- Missing idempotency keys on charge/refund API calls
- Webhook signature verification missing or incorrect
- Payment amount validated client-side but not server-side
- Refund amount not validated against original charge
- Missing reconciliation between local records and processor state
- Race conditions on payment status updates (double processing)
- Currency mismatch between what user sees and what is charged

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
