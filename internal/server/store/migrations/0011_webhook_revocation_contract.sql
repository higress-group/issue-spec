ALTER TABLE webhook_subscriptions
    ADD COLUMN revoked_at timestamptz;

ALTER TABLE webhook_subscriptions
    ADD CONSTRAINT webhook_subscriptions_revocation_consistent CHECK (
        revoked_at IS NULL OR (NOT active AND revoked_at >= created_at)
    );

CREATE FUNCTION prevent_webhook_subscription_unrevoke()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
    IF OLD.revoked_at IS NOT NULL AND (
        NEW.revoked_at IS DISTINCT FROM OLD.revoked_at OR NEW.active
    ) THEN
        RAISE EXCEPTION 'revoked webhook subscriptions are immutable'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$function$;

CREATE TRIGGER webhook_subscriptions_prevent_unrevoke
BEFORE UPDATE ON webhook_subscriptions
FOR EACH ROW EXECUTE FUNCTION prevent_webhook_subscription_unrevoke();

COMMENT ON COLUMN webhook_subscriptions.revoked_at IS
    'Irreversible terminal lifecycle marker. NULL means active or resumable pause.';
