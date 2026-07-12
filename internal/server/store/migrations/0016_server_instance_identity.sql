CREATE TABLE server_instance_identity (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    instance_id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

INSERT INTO server_instance_identity (singleton) VALUES (true)
ON CONFLICT (singleton) DO NOTHING;
