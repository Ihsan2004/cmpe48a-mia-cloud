# Kubernetes Manifests (GKE)

This directory will contain Kubernetes Deployments, Services, Ingress, ConfigMaps/Secrets templates, and HorizontalPodAutoscaler (HPA) manifests for:

- `frontend` (React + Nginx)
- `backend` (Go Fiber API + Post-Meeting Workers, HPA enabled)
- `mia-search` (Python FastAPI NLP/Embedding service, HPA enabled)

> Scheduled for Phase 3 (GKE Deployment & Autoscaling).
