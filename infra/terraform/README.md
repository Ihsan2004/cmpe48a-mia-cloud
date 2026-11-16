# Terraform Infrastructure (IaC)

This directory will hold the Terraform configurations (Bonus +5%) for provisioning all GCP resources in `europe-west1`:

- VPC network and private connectivity
- Artifact Registry repository for service container images
- Google Kubernetes Engine (GKE) zonal cluster
- Google Compute Engine (GCE) VM for `meeting-bot`
- Cloud SQL for PostgreSQL instance (`pgvector` enabled)
- Google Cloud Functions (2nd Gen) deployment for `brief-notifier`

> Scheduled for Phase 3 (GCP Infrastructure Provisioning).
