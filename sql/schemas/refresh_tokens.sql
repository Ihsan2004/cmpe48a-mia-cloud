CREATE TABLE public.refresh_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    token TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked BOOLEAN DEFAULT false,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_refresh_tokens_user_id ON public.refresh_tokens (user_id);
CREATE INDEX idx_refresh_tokens_token ON public.refresh_tokens (token);
CREATE INDEX idx_refresh_tokens_expires_at ON public.refresh_tokens (expires_at);
CREATE INDEX idx_refresh_tokens_revoked ON public.refresh_tokens (revoked) WHERE revoked = false;
