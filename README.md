# MIA — Cloud-Native Meeting Intelligence Platform (CMPE 48A Term Project)

**Course:** CMPE 48A — Cloud Computing  
**Student:** Faik İhsan Südüpak (`2022400084`)  
**Project Type:** Individual Term Project

## Overview

**MIA** is an AI-powered meeting intelligence and pre-meeting briefing platform. An automated browser bot joins online meetings (Google Meet, Microsoft Teams, Zoom), filters silence via local Voice Activity Detection (VAD), streams audio in real time over WebSocket to the **ElevenLabs Scribe v2 Realtime API**, and writes speaker-attributed `transcript_segments` directly to PostgreSQL (`pgvector`). Once the meeting ends, the Go backend orchestrates transcript correction, OpenAI summarization, decision extraction, vector indexing, and pre-meeting brief delivery via a serverless Google Cloud Function (`brief-notifier`).

All Supabase runtime/build dependencies have been removed so the entire stack runs natively on standard PostgreSQL + `pgvector` locally (`docker compose`) and on **Google Cloud Platform (GCP)**.

## Local Quickstart (Single Command with Docker Compose)

Every service builds from its own isolated `Dockerfile` and connects via standard environment variables.

### 1. Configure Environment
```bash
cp env.example .env
```
Open `.env` and fill in your secrets:
- `JWT_SECRET` (at least 32 random characters)
- `INTERNAL_SERVICE_SHARED_SECRET`, `MIA_BOT_API_KEY` (random strings for internal service authentication)
- `OPENAI_API_KEY` (for summarization, extraction, and embeddings)
- `ELEVENLABS_API_KEY` (for real-time STT inside `meeting-bot`)
- *(Optional)* `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` (for Google Calendar OAuth) and `SMTP_*` (otherwise `brief-notifier` runs in dry-run mode)

### 2. Start All Services
```bash
docker compose up --build
```
PostgreSQL (`pgvector/pgvector:pg16`) initializes automatically from `sql/000_init_postgres.sql`.

### 3. Open the App & Create an Account
- **Web Dashboard:** `http://localhost:3000`
- **Signup / Login:** Native email + password signup (`POST /api/auth/signup`) works immediately without Google OAuth or SMTP verification; accept the consent checkboxes on `/signup` to enter the dashboard.
- **API Health Checks:**
  - `mia-backend`: `http://localhost:8080/health` (also proxied via `http://localhost:3000/api/...`)
  - `mia-search`: `http://localhost:8000/health`
  - `meeting-bot`: `http://localhost:3001/health`
  - `brief-notifier`: `http://localhost:8081/health`

## Repository Layout

```text
cmpe48a-mia-cloud/
├── docker-compose.yml             # Local full-stack orchestration (6 services)
├── env.example                    # Sanitized environment template (zero secrets)
├── services/
│   ├── backend/                   # Go Fiber API, auth, calendar sync, bot dispatcher, workers
│   ├── frontend/                  # React + Vite SPA + Nginx same-origin reverse proxy
│   ├── mia-search/                # Python FastAPI service (psycopg + pgvector + OpenAI)
│   ├── meeting-bot/               # Playwright + Chromium + VAD + ElevenLabs Live WS STT
│   └── cloud-functions/
│       └── brief-notifier/        # Google Cloud Functions 2nd Gen HTTP brief notifier
├── sql/
│   └── 000_init_postgres.sql      # Standalone PostgreSQL 16 + pgvector schema
├── infra/
│   ├── terraform/                 # GCP Infrastructure-as-Code (Phase 3)
│   └── k8s/                       # GKE Deployments, Services, Ingress, HPA manifests (Phase 3)
├── locust/                        # Locust performance test scripts (Phase 4)
└── docs/                          # Architecture diagram, Phase 1 PDF report & plan
```

## Mapping to GCP Architecture (CMPE 48A)

The exact same per-service `Dockerfile` images and environment variables map cleanly to the required GCP compute paradigms without code changes:

| Service | Local (`docker-compose.yml`) | GCP Target Deployment |
|---|---|---|
| `mia-frontend`, `mia-backend`, `mia-search` | Separate containers on `mia-network` | **GKE (Kubernetes)** Deployments + Services + Ingress + **HPA** (`BACKEND_UPSTREAM` & `NGINX_RESOLVER` configured for `kube-dns`) |
| `meeting-bot` | Container with `Xvfb` + `PulseAudio` (no `/var/run/docker.sock`) | Dedicated **Compute Engine (GCE) VM** (`e2-medium`) running the bot container; reached via `EXTERNAL_BOT_API_URL` |
| `brief-notifier` | Local `functions-framework` container | **Google Cloud Functions (2nd Gen)** (`gcloud functions deploy brief-notifier --gen2 --runtime=python312`) reached via `BRIEF_NOTIFIER_FUNCTION_URL` |
| PostgreSQL + `pgvector` | `pgvector/pgvector:pg16` container | **Cloud SQL for PostgreSQL 16** (`pgvector` enabled) reached via `DATABASE_URL` |
