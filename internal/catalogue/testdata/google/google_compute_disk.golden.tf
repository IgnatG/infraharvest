# google_compute_disk, from data/google/compute.yaml

import {
  to = google_compute_disk.data
  id = "projects/acme-prod/zones/us-central1-a/disks/data"
}

# projects/acme-prod/zones/us-central1-a/disks/gke-prod-default-pool-1a2b3c4d-x9z8: left out by default, would import as "projects/acme-prod/zones/us-central1-a/disks/gke-prod-default-pool-1a2b3c4d-x9z8": GKE creates it for a cluster's nodes and deletes it with them
# projects/acme-prod/zones/us-central1-a/disks/pvc-0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d: left out by default, would import as "projects/acme-prod/zones/us-central1-a/disks/pvc-0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d": GKE creates it for a persistent volume claim and deletes it with the claim
# projects/acme-prod/zones/us-central1-a/disks/web: left out by default, would import as "projects/acme-prod/zones/us-central1-a/disks/web": the boot disk of an instance, which manages it in its boot_disk block; select it to import it
# projects/acme-prod/zones/us-central1-b/disks/batch: left out by default, would import as "projects/acme-prod/zones/us-central1-b/disks/batch": it may be the boot disk of an instance, as the project's instances couldn't be listed; select it to import it
