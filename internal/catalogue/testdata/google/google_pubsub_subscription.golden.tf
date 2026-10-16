# google_pubsub_subscription, from data/google/pubsub.yaml

import {
  to = google_pubsub_subscription.orders_billing
  id = "projects/acme-prod/subscriptions/orders-billing"
}

# Left out where the plan stays the same, as the default: ack_deadline_seconds=10, enable_exactly_once_delivery=false, enable_message_ordering=false, expiration_policy.ttl=2678400s, message_retention_duration=604800s, retain_acked_messages=false
