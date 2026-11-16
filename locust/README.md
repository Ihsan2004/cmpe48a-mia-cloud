# Locust Load & Performance Tests

This directory will contain `locustfile.py` scenarios simulating realistic user traffic against the GCP deployment (API endpoints, semantic search, brief retrieval, and Cloud Function invocations) to measure:

- Request latency (p50 / p95 / p99)
- Throughput (RPS)
- CPU & memory utilization + GKE HPA pod scale-out behavior
- Error rates under load

> Scheduled for Phase 4 (Performance Evaluation).
