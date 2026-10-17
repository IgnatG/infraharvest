# google_pubsub_subscription, from data/google/pubsub.yaml

import {
  to = google_pubsub_subscription.orders_billing
  id = "projects/acme-prod/subscriptions/orders-billing"
}

# projects/acme-prod/subscriptions/eventarc-us-central1-thumbnail-123456-sub-347: left out by default, would import as "projects/acme-prod/subscriptions/eventarc-us-central1-thumbnail-123456-sub-347": Eventarc creates it for a trigger, such as a 2nd gen function's, and deletes it with the trigger
# projects/acme-prod/subscriptions/gcf-resize-us-central1-uploads: left out by default, would import as "projects/acme-prod/subscriptions/gcf-resize-us-central1-uploads": Cloud Functions creates it for a function's Pub/Sub trigger and deletes it with the function

# Left out where the plan stays the same, as the default: ack_deadline_seconds=10, enable_exactly_once_delivery=false, enable_message_ordering=false, expiration_policy.ttl=2678400s, message_retention_duration=604800s, retain_acked_messages=false
