CREATE TABLE public.calendar_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL UNIQUE,
    resource_id TEXT NOT NULL,
    expiration TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_calendar_subscriptions_user_id ON public.calendar_subscriptions (user_id);
CREATE INDEX idx_calendar_subscriptions_channel_id ON public.calendar_subscriptions (channel_id);
CREATE INDEX idx_calendar_subscriptions_expiration ON public.calendar_subscriptions (expiration);

CREATE OR REPLACE FUNCTION public.update_calendar_subscriptions_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER calendar_subscriptions_updated_at
    BEFORE UPDATE ON public.calendar_subscriptions
    FOR EACH ROW EXECUTE FUNCTION update_calendar_subscriptions_updated_at();
