# `brief-notifier` (Google Cloud Functions - 2nd Gen)

Stateless HTTP-triggered serverless function responsible for delivering pre-meeting briefs and post-meeting summary notifications (Email / Slack) when invoked by `mia-backend`.

## Planned Deployment (Phase 3)

```bash
gcloud functions deploy brief-notifier \
  --gen2 \
  --runtime=python312 \
  --region=europe-west1 \
  --source=. \
  --entry-point=brief_notifier \
  --trigger-http \
  --no-allow-unauthenticated
```
