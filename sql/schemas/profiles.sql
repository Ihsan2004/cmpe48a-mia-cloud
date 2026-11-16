CREATE TABLE public.profiles (
    id UUID PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    full_name TEXT,
    email TEXT UNIQUE,
    job_title TEXT,
    company TEXT,
    timezone TEXT DEFAULT 'UTC',
    default_meeting_duration INTEGER DEFAULT 30,
    work_start_hour TEXT,
    work_end_hour TEXT,
    bot_name TEXT DEFAULT 'MIA Assistant',
    bot_language TEXT DEFAULT 'tr',
    google_refresh_token TEXT,
    is_calendar_connected BOOLEAN DEFAULT false,
    google_email TEXT,
    auto_share_meetings BOOLEAN DEFAULT false,
    interface_language TEXT DEFAULT 'tr',
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_profiles_google_refresh_token ON public.profiles (google_refresh_token) WHERE google_refresh_token IS NOT NULL;
CREATE INDEX idx_profiles_calendar_connected ON public.profiles (is_calendar_connected) WHERE is_calendar_connected = true;
CREATE INDEX idx_profiles_google_email ON public.profiles (google_email) WHERE google_email IS NOT NULL;

-- Auto-update is_calendar_connected when refresh token changes
CREATE OR REPLACE FUNCTION public.update_calendar_connection_status()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.google_refresh_token IS NOT NULL AND NEW.google_refresh_token != '' THEN
        NEW.is_calendar_connected = true;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER on_refresh_token_change
    BEFORE UPDATE OF google_refresh_token ON public.profiles
    FOR EACH ROW EXECUTE FUNCTION update_calendar_connection_status();

-- Auto-create profile when user is created
CREATE OR REPLACE FUNCTION public.handle_new_user()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO public.profiles (id, full_name, email)
    VALUES (NEW.id, NEW.full_name, NEW.email)
    ON CONFLICT (id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER on_user_created
    AFTER INSERT ON public.users
    FOR EACH ROW EXECUTE FUNCTION handle_new_user();
