# MIA — Cloud-Native Meeting Intelligence Platform (CMPE 48A Term Project)

**Course:** CMPE 48A — Cloud Computing  
**Student:** Faik İhsan Südüpak (`2022400084`)  
**Project Type:** Individual Term Project (Phase 1 — Architecture & Codebase Setup)

## Overview

**MIA** is an AI-powered meeting intelligence and pre-meeting briefing platform. A browser-based bot joins online meetings (Google Meet, Microsoft Teams, Zoom), performs real-time speech-to-text transcription via the **ElevenLabs Realtime WebSocket API**, writes speaker-attributed segments to PostgreSQL, and triggers an automated post-meeting NLP pipeline (transcript correction, summarization, decision extraction, semantic embeddings, and pre-meeting brief delivery).

For CMPE 48A, MIA is structured around a multi-paradigm **Google Cloud Platform (GCP)** architecture combining **Kubernetes (GKE + HPA)**, **Virtual Machines (GCE)**, **Cloud SQL (PostgreSQL + `pgvector`)**, and **Serverless Functions (Cloud Functions 2nd Gen)**.

## Planned Cloud Architecture (GCP)

| GCP Service / Paradigm | Hosted Component(s) | Functional Role |
|---|---|---|
| **GKE (Kubernetes + HPA)** | `frontend` (React/Nginx), `backend` (Go Fiber), `mia-search` (Python FastAPI) | Stateless web UI, REST API, post-meeting orchestration, and NLP/vector search microservices with Horizontal Pod Autoscaling. |
| **Compute Engine (GCE VM)** | `meeting-bot` (Node.js/TypeScript, Playwright, Chromium, Xvfb, ONNX VAD) | Long-lived, stateful WebRTC browser sessions and real-time PCM audio streaming to ElevenLabs Live WebSocket STT. |
| **Cloud SQL (PostgreSQL)** | Relational schema + `pgvector` embeddings (`sql/`) | Managed persistence for users, meetings, live `transcript_segments`, summaries, decision items, and vector embeddings. |
| **Cloud Functions (2nd Gen)** | `brief-notifier` (Python HTTP function) | Event-driven serverless delivery of pre-meeting briefs and post-meeting alerts via Email (SMTP) / Slack. |

## Repository Layout

```text
cmpe48a-mia-cloud/
├── services/
│   ├── backend/                   # Go Fiber API, calendar sync, bot dispatcher, post-meeting pipeline
│   ├── frontend/                  # React + Vite SPA
│   ├── mia-search/                # Python FastAPI service (summarization, OpenAI embeddings, RAG search)
│   ├── meeting-bot/               # Playwright meeting bot (ElevenLabs Scribe v2 Realtime WebSocket STT + VAD)
│   └── cloud-functions/
│       └── brief-notifier/        # Google Cloud Functions 2nd Gen HTTP brief notification handler
├── sql/                           # PostgreSQL + pgvector schemas and migrations
├── infra/
│   ├── terraform/                 # GCP Infrastructure-as-Code (Phase 3)
│   └── k8s/                       # GKE Deployments, Services, Ingress, HPA manifests (Phase 3)
├── locust/                        # Locust performance test scripts (Phase 4)
├── docs/                          # Term project documentation & Phase 1 report
└── env.example                    # Sanitized environment variable template (no real secrets)
```

## Real-Time Transcription Flow (ElevenLabs Live API)

1. `backend` schedules/triggers `meeting-bot` via HTTP (`HTTPBotDispatcher` / `EXTERNAL_BOT_API_URL`).
2. `meeting-bot` joins the meeting in headless Chromium (`Xvfb` + PulseAudio), applies hybrid VAD (`@ricky0123/vad-node` / `webrtcvad`), and streams PCM audio over WebSocket to **ElevenLabs Scribe v2 Realtime**.
3. `meeting-bot` resolves active speakers (WebRTC + DOM detection) and writes `transcript_segments` to PostgreSQL, then notifies `backend` via `POST /api/webhooks/bot-completion` (`LEGACY_BOT_COMPLETION_TRIGGER=true`).
4. `backend` runs transcript correction, calls `mia-search` for summarization & `pgvector` embeddings, extracts decision items, and invokes `brief-notifier` (Cloud Function) when briefs are ready.

## Project Roadmap

- [x] **Phase 1:** Monorepo consolidation, real-time bot restoration, tentative GCP architecture & cost estimation
- [ ] **Phase 2:** Cloud SQL (`psycopg`) adaptation in `mia-search`, end-to-end live-bot integration & local Docker verification
- [ ] **Phase 3:** Terraform IaC provisioning + GKE Deployments/HPA + GCE Bot VM + Cloud Function deployment + CI/CD
- [ ] **Phase 4:** Locust load testing, latency/throughput/CPU/error analysis & final report + demo video
