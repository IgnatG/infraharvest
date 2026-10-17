# google_compute_region_disk, from data/google/compute.yaml

import {
  to = google_compute_region_disk.shared
  id = "projects/acme-prod/regions/us-central1/disks/shared"
}

# projects/acme-prod/regions/us-central1/disks/pvc-1a2b3c4d-4e5f-6a7b-8c9d-0e1f2a3b4c5d: left out by default, would import as "projects/acme-prod/regions/us-central1/disks/pvc-1a2b3c4d-4e5f-6a7b-8c9d-0e1f2a3b4c5d": GKE creates it for a persistent volume claim and deletes it with the claim
# projects/acme-prod/regions/us-central1/disks/db-boot: left out by default, would import as "projects/acme-prod/regions/us-central1/disks/db-boot": the boot disk of an instance, which manages it in its boot_disk block; select it to import it
