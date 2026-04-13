# marketing-service

Email marketing, e-commerce, and conversion funnel service. Handles the funnel builder, product and offer management, coupon/discount logic, contact management, and outbound webhook triggers.

**Port:** `8082`

## Responsibilities

- Funnel builder and page serving (domain-matched routes)
- Funnel event ingestion (visitor actions, conversion triggers)
- Product, offer, and coupon CRUD
- Contact management and segmentation
- Customer-facing product library
- Outbound webhooks to external platforms
- Legacy route compatibility for older frontend paths

## Directory Structure

```
marketing-service/
├── cmd/
│   └── main.go               # Entry point
├── routes/
│   ├── funnel.go             # Funnel CRUD and trigger logic
│   ├── ecommerce.go          # Products, offers, coupons, contacts, purchases
│   ├── email.go              # Email campaign integration
│   ├── outbound_webhooks.go  # Webhook triggers and integrations
│   └── internal.go           # Service-to-service endpoints
├── queries/
│   └── queries.go            # MongoDB query layer
└── email/                    # Email template utilities
```

## API Endpoints

### Public (no auth)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/marketing/funnel/page` | Serve a funnel page by domain/path |
| `POST` | `/api/marketing/funnel/event` | Ingest a visitor funnel event |
| `GET` | `/api/marketing/products` | Public product listing |
| `GET` | `/api/marketing/purchases` | Purchase history (public) |

### Tenant (JWT required)

| Method | Path | Description |
|--------|------|-------------|
| `GET/POST` | `/api/marketing/tenant/products` | List and create products |
| `GET/PUT/DELETE` | `/api/marketing/tenant/products/:id` | Product operations |
| `GET/POST` | `/api/marketing/tenant/offers` | List and create offers |
| `GET/PUT/DELETE` | `/api/marketing/tenant/offers/:id` | Offer operations |
| `GET/POST` | `/api/marketing/tenant/coupons` | List and create coupons |
| `GET/PUT/DELETE` | `/api/marketing/tenant/coupons/:id` | Coupon operations |
| `GET` | `/api/marketing/tenant/contacts` | Contact list and search |

### Customer (JWT required)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/customer/library/products` | Customer-visible product library |

### Legacy paths (JWT required)

`/api/tenant/*` and `/api/funnel/*` are kept for backward compatibility and mirror the `/api/marketing/tenant/` routes above.

### Internal (`/internal/`, no auth)

Service-to-service queries called by core-service and other internal services.

## Data Models

All models live in `pkg/models/`.

**`Funnel`** — A top-level marketing funnel.
- `name`, `domain`, `routes[]`, `start_trigger`, `complete_trigger`, `ai_context`

**`FunnelRoute`** — A badge-gated branch within a funnel.
- `name`, `order`, `required_badges[]`, `stages[]`

**`FunnelStage`** — A visual/conversion step in a route (landing page, order form, etc.).

**`Product`** — A sellable item (course, book, membership, etc.).
- `name`, `description`, `price`, `status`, `course_modules[]`, `enrollment_count`

**`Offer`** — A limited-time deal or promotion.
- `name`, `product_id`, `discount`, `expiry`

**`Coupon`** — A discount code tied to an offer.
- `code`, `offer_id`, `max_uses`, `redeemed_count`

**`User`** — A contact/subscriber record.
- `email`, `name`, `badges[]`, `subscribed`

**`Purchase`** — A transaction record.
- `user_id`, `product_id`, `amount`, `created_at`

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `MARKETING_SERVICE_PORT` | `8082` | HTTP listen port |
| `MONGO_HOST` | — | MongoDB host |
| `MONGO_PORT` | — | MongoDB port |
| `MONGO_DB` | — | Database name |

## Dependencies

- [`gin-gonic/gin`](https://github.com/gin-gonic/gin) — HTTP framework
- `gopkg.in/mgo.v2` — MongoDB driver
- `../pkg` — Shared auth, config, db, models, HTTP utilities
