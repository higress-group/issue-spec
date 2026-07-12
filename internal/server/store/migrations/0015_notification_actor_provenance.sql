ALTER TABLE webhook_subscriptions
    DROP CONSTRAINT webhook_subscriptions_actor_classes_valid,
    ADD CONSTRAINT webhook_subscriptions_actor_classes_valid CHECK (
        actor_classes <@ ARRAY['human','automation']::text[]
    );
