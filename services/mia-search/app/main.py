"""
FastAPI-based NLP Microservice for MIA Meeting Analysis
Main entry point for the application.

Features:
- Internal Context Search (OpenAI 5.2 semantic search within transcripts)
- Proactive Web Search (SpaCy + OpenAI 5.2 + Perplexity pipeline)
- Meeting Summarization
- Research Topic Detection
"""
from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import JSONResponse
import os
import logging
import secrets
import uvicorn

from app.routers import health, summarize, embeddings, extraction

logger = logging.getLogger(__name__)

INTERNAL_SERVICE_SECRET_HEADER = "X-Internal-Service-Secret"
PUBLIC_PATHS = {
    "/",
    "/health",
    "/openapi.json",
    "/docs",
    "/docs/oauth2-redirect",
    "/redoc",
}

# Create FastAPI app
app = FastAPI(
    title="MIA Search Service",
    description="Enhanced NLP Microservice for Meeting Analysis with Proactive Web Search",
    version="2.0.1"
)

# CORS configuration
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],  # Configure based on CORS_ORIGINS env
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.middleware("http")
async def enforce_internal_service_auth(request: Request, call_next):
    """Require shared-secret auth for backend -> mia-search internal calls."""
    path = request.url.path
    if path in PUBLIC_PATHS:
        return await call_next(request)

    expected_secret = os.getenv("INTERNAL_SERVICE_SHARED_SECRET", "").strip()
    if not expected_secret:
        logger.error("INTERNAL_SERVICE_SHARED_SECRET is not configured")
        return JSONResponse(
            status_code=503,
            content={"detail": "Internal service authentication is not configured"},
        )

    provided_secret = (request.headers.get(INTERNAL_SERVICE_SECRET_HEADER) or "").strip()
    if not provided_secret or not secrets.compare_digest(provided_secret, expected_secret):
        return JSONResponse(
            status_code=401,
            content={"detail": "Unauthorized internal service request"},
        )

    return await call_next(request)


# Include routers
app.include_router(health.router, tags=["health"])
app.include_router(summarize.router, tags=["summarization"])
app.include_router(embeddings.router, tags=["embeddings"])
app.include_router(extraction.router, tags=["extraction"])


if __name__ == "__main__":


    port = int(os.getenv("PORT", 8000))
    uvicorn.run(
        "app.main:app",
        host="0.0.0.0",
        port=port,
        reload=False
    )
