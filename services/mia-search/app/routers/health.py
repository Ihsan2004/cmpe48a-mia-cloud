"""Health check router."""
from fastapi import APIRouter
from datetime import datetime

router = APIRouter()


@router.get("/health")
def health_check():
    """Health check endpoint."""
    return {
        "status": "healthy",
        "service": "mia-search",
        "timestamp": datetime.utcnow().isoformat()
    }


@router.get("/")
def root():
    """Root endpoint - service info."""
    return {
        "service": "MIA Search Service",
        "version": "1.0.0",
        "status": "running",
        "endpoints": ["/search", "/catch-up", "/summarize", "/research/detect", "/research/topics/{meeting_id}", "/research/execute", "/health"]
    }

