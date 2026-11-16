# Implementation Plan: Local Docker Portability, Supabase Removal & GCP Readiness

**Repository:** `Ihsan2004/cmpe48a-mia-cloud`  
**Objective:**
1. Make the entire MIA platform runnable locally with a single `docker compose up --build` command (after filling `.env`), while keeping every service image and configuration 100% compatible with the target GCP architecture (**GKE + HPA**, **GCE Bot VM**, **Cloud SQL PostgreSQL + `pgvector`**, **Google Cloud Functions 2nd Gen**).
2. Remove all Supabase dependencies and `.env` variables cleanly across `mia-search`, `frontend`, `backend`, `sql/`, and `env.example` without startup or runtime errors.

---

## Phase 1: Complete Supabase Removal & Pure PostgreSQL (`pgvector`) Alignment

### 1.1 `services/mia-search` (`supabase-py` → `psycopg`)
- **Files:**
  - `services/mia-search/Dockerfile`
  - `services/mia-search/requirements.txt`
  - `services/mia-search/app/db.py`
  - `services/mia-search/app/repositories/transcript_repo.py`
  - `services/mia-search/app/repositories/embedding_repo.py`
  - `services/mia-search/tests/`
- **Changes:**
  - Upgrade base image in `services/mia-search/Dockerfile` from `python:3.9-slim` (EOL) to `python:3.11-slim`.
  - Replace `supabase` in `requirements.txt` with `psycopg[binary]>=3.1.0,<3.3.0`.
  - Rewrite `app/db.py` to provide `get_db_connection()` using `DATABASE_URL` and `psycopg.rows.dict_row`, completely removing `SUPABASE_URL` and `SUPABASE_KEY`.
  - Rewrite `TranscriptRepository.fetch_segments()` and `TranscriptRepository.fetch_blocks()` with parameterized SQL:
    - Validate `uuid.UUID(meeting_id)` before adding `meeting_id = %s` (preventing PostgreSQL `InvalidTextRepresentation` when non-UUID IDs or `meeting_url` fallback is used).
    - Update `_filter_by_time()` and formatting helpers to handle native `datetime` / `UUID` objects returned by `psycopg` (converting cleanly without assuming strings).
  - Rewrite `EmbeddingRepository` methods (`count_blocks_without_embedding`, `fetch_blocks_without_embedding`, `save_embedding`, `mark_too_short`, `check_meeting_has_embeddings`, `semantic_search`) using parameterized SQL:
    - Use `embedding_model IS DISTINCT FROM 'too_short'` instead of `!=` so `NULL` rows are included.
    - Serialize float lists as pgvector text literals (`f"[{','.join(map(str, vec))}]"`) cast with `%s::vector` for `save_embedding` and `semantic_search` (`1 - (embedding <=> %s::vector) >= %s`).
  - Add/update pytest unit tests covering SQL repos and `_filter_by_time` with native `datetime`/`UUID` objects.

### 1.2 `services/frontend` (`@supabase/supabase-js` removal)
- **Files:**
  - `services/frontend/package.json`
  - `services/frontend/Dockerfile`
  - `services/frontend/src/services/supabase.js`
  - `services/frontend/src/pages/Dashboard.jsx`
- **Changes:**
  - Remove `VITE_SUPABASE_URL` and `VITE_SUPABASE_ANON_KEY` build args/env vars from `services/frontend/Dockerfile`.
  - Remove `src/services/supabase.js` (which threw on missing Supabase env vars) and remove the unused Supabase realtime subscription from `Dashboard.jsx` (replacing with clean periodic refresh or no-op cleanup), and remove `@supabase/supabase-js` from `package.json`.
  - Verify `npm run build` and `npm run test` succeed.

### 1.3 `services/backend` (Batch Guard Removal, CORS/Auth Alignment & Cloud Function Wiring)
- **Files:**
  - `services/backend/cmd/server/main.go`
  - `services/backend/internal/config/config.go`
  - `services/backend/internal/handlers/bot.go`
  - `services/backend/internal/services/bot_dispatcher.go`
  - `services/backend/internal/services/briefnotify/` (Cloud Function HTTP deliverer)
- **Changes:**
  - Remove the batch-only `log.Fatalf` startup guard on `ELEVENLABS_API_KEY` without `ELEVENLABS_WEBHOOK_ID`/`ELEVENLABS_WEBHOOK_SECRET` in `cmd/server/main.go` (log a warning and skip batch transcription instead).
  - Prevent Fiber CORS panic when `CORS_ORIGINS` is unset by defaulting `CORSOrigins` in `config.go` to `"http://localhost:3000"` and guarding empty origins in `main.go`.
  - Standardize bot API key resolution: let `bot.go` and `bot_dispatcher.go` fall back from `EXTERNAL_BOT_API_KEY` to `MIA_BOT_API_KEY` / `BOT_API_KEY` (matching `middleware/bot_auth.go`), so one shared `MIA_BOT_API_KEY` works across both services.
  - Add `BriefNotifierFunctionURL` (`BRIEF_NOTIFIER_FUNCTION_URL`) and `InternalServiceSharedSecret` (`INTERNAL_SERVICE_SHARED_SECRET`) to `config.go`, and wire a `CloudFunctionBriefDeliverer` (implementing `briefnotify.BriefDeliverer` for `"email"` channel, taking precedence over direct SMTP when `BRIEF_NOTIFIER_FUNCTION_URL` is set).
  - Verify `go test -short ./...` and `go vet ./...` pass.

### 1.4 Standalone PostgreSQL Schema & Clean `env.example`
- **Files:**
  - `sql/000_init_postgres.sql`
  - `env.example`
- **Changes:**
  - Generate `sql/000_init_postgres.sql` from `sql/init_test_db.sql` + required migrations (`009`, `010`, `014`–`018`, `020_user_consents`, `022`) so it runs cleanly on vanilla `pgvector/pgvector:pg16` and GCP Cloud SQL:
    - Strip PG17-only `SET transaction_timeout = 0;` and reset `search_path` safely (`SET search_path TO public;`).
    - Replace `CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA extensions;` with `CREATE EXTENSION IF NOT EXISTS pgcrypto;` + `CREATE EXTENSION IF NOT EXISTS vector;`.
    - Remove empty-policy `ENABLE ROW LEVEL SECURITY` statements on internal backend tables.
    - Ensure `public.user_consents`, `profiles.teams_onboarding_status`, `teams`, `team_members`, `team_detection_runs`, `meetings_user_google_event_unique`, and `transcript_segments.role` default are included.
  - Remove all `SUPABASE_*` and `VITE_SUPABASE_*` keys from `env.example`, drop non-existent `JWT_REFRESH_SECRET`, keep `NLP_SERVICE_URL` (what Go services actually read), and document `MIA_BOT_API_KEY`, `LEGACY_BOT_COMPLETION_TRIGGER`, and `BRIEF_NOTIFIER_FUNCTION_URL`.

---

## Phase 2: `meeting-bot` In-Container Live Mode (No DooD) & `brief-notifier` Containerization

### 2.1 `services/meeting-bot` (Remove `/var/run/docker.sock` Dependency)
- **Files:**
  - `services/meeting-bot/src/services/containerManager.ts`
  - `services/meeting-bot/src/services/globalContainerManager.ts`
  - `services/meeting-bot/src/app/index.ts`
  - `services/meeting-bot/src/config.ts`
  - `services/meeting-bot/Dockerfile.production`
  - `services/meeting-bot/.env.example`
- **Changes:**
  - Replace Dockerode sibling-container spawning in `ContainerManager` with an in-container child-process runner spawning `node dist/cli.js` (`detached: true`, sharing container `:99` Xvfb display and PulseAudio `virtual_output.monitor`), enforcing `MAX_CONCURRENT_MEETINGS=1` by default in single-container mode to prevent X11/PulseAudio stream collisions across concurrent meetings.
  - Ensure `stopMeeting()`, `force` replacement, and `shutdown()` terminate the entire child process group (`process.kill(-pid, 'SIGTERM')` → `'SIGKILL'`) so Chromium, `ffmpeg`, and `parec` never leak as orphan processes.
  - Update `GET /health` in `src/app/index.ts` to report `200 healthy` without requiring `/var/run/docker.sock`.
  - Update `services/meeting-bot/.env.example` for ElevenLabs Live API (`STT_ENABLED=true`, `TRANSCRIPT_WRITE_ENABLED=true`, `VAD_ENABLED=true`, `MAX_CONCURRENT_MEETINGS=1`, `MIA_BACKEND_URL=http://mia-backend:8080`, `DATABASE_SSL=false`).
  - Verify `npm run build` succeeds cleanly.

### 2.2 `services/cloud-functions/brief-notifier`
- **Files:**
  - `services/cloud-functions/brief-notifier/main.py`
  - `services/cloud-functions/brief-notifier/requirements.txt`
  - `services/cloud-functions/brief-notifier/Dockerfile` (for local Docker Compose parity; ignored by `gcloud functions deploy`)
  - `services/cloud-functions/brief-notifier/test_main.py`
- **Changes:**
  - Handle `GET /health` with `200 {"status": "healthy"}`.
  - Support multipart (`text` + `html`) email payloads via `EmailMessage.add_alternative(..., subtype="html")` when `html` is provided, alongside `X-Internal-Service-Secret` validation and dry-run mode when SMTP is unconfigured.
  - Add pytest unit tests for `/health`, auth checks, validation, and dry-run/multipart payload handling.

---

## Phase 3: Unified Root `docker-compose.yml`, Same-Origin Frontend Proxy & Documentation

### 3.1 Same-Origin Nginx Proxy (Zero-Rebuild Local ↔ GKE Portability)
- **Files:**
  - `services/frontend/nginx.conf.template` (or `nginx.conf`)
  - `services/frontend/Dockerfile`
- **Changes:**
  - Use `nginx:alpine`'s `/etc/nginx/templates/default.conf.template` with configurable `BACKEND_UPSTREAM` (default `http://mia-backend:8080`) and `NGINX_RESOLVER` (default `127.0.0.11` for Docker, overrideable to `kube-dns.kube-system.svc.cluster.local` on GKE):
    - `location ^~ /api/` with `proxy_pass $backend_upstream;` (no trailing `/`, preserving `/api/*`), forwarding `Host`, `X-Real-IP`, `X-Forwarded-For`, `X-Forwarded-Proto`, `proxy_read_timeout 300s`, `client_max_body_size 50m`.
    - `location ^~ /ws/` with WebSocket `Upgrade` / `Connection "upgrade"` headers.
    - Keep `location / { try_files $uri $uri/ /index.html; }` SPA fallback.
  - Default `VITE_API_URL=/api` in `Dockerfile` and `docker-compose.yml`.

### 3.2 Root `docker-compose.yml` & `README.md`
- **Files:**
  - `docker-compose.yml`
  - `README.md`
- **Changes:**
  - Create root `docker-compose.yml` orchestrating `postgres` (`pgvector/pgvector:pg16` with `./sql/000_init_postgres.sql`), `mia-search`, `brief-notifier`, `mia-backend`, `meeting-bot`, and `frontend` on `mia-network`, using each service's own separate `Dockerfile`.
  - Update `README.md` with step-by-step instructions for local Docker Compose setup (including email/password signup & optional Google OAuth) and GCP deployment mapping (GKE + HPA, GCE VM, Cloud SQL, Cloud Functions 2nd Gen).

---

## Phase 4: Live Docker Compose Build, Smoke Tests, Secret Audit & Final Repo Sync

### 4.1 Local Docker Compose End-to-End Verification
- Build all 6 service images via `docker compose build`.
- Boot the full stack with a temporary local `.env` (never committed!) and verify:
  - `postgres` initializes `000_init_postgres.sql` with zero errors (`pgvector` + all tables including `user_consents` and `teams`).
  - All 6 containers reach running/healthy state.
  - Smoke-test `POST /api/auth/signup` (with all 6 mandatory `consents`), `POST /api/auth/signin`, `GET /api/auth/me`, `GET /api/meetings`, `mia-search /health`, `meeting-bot /health`, `brief-notifier /health` + dry-run POST, and frontend `/` + `/api/` proxy.
- Run `docker compose down -v` and delete the temporary `.env` after verification.

### 4.2 Final Secret Audit, Review & Push
- Run regex secret & `.env` audit across all tracked files (`git ls-files`).
- Run unbiased subagent review on the final repository state.
- Commit and push to `origin/main` (`https://github.com/Ihsan2004/cmpe48a-mia-cloud.git`) and verify `HEAD == origin/main`.
