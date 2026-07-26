CREATE FUNCTION refresh_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$;

CREATE TRIGGER administrators_refresh_updated_at
BEFORE UPDATE ON administrators
FOR EACH ROW
EXECUTE FUNCTION refresh_updated_at();

CREATE TRIGGER system_settings_refresh_updated_at
BEFORE UPDATE ON system_settings
FOR EACH ROW
EXECUTE FUNCTION refresh_updated_at();

CREATE TRIGGER model_providers_refresh_updated_at
BEFORE UPDATE ON model_providers
FOR EACH ROW
EXECUTE FUNCTION refresh_updated_at();
