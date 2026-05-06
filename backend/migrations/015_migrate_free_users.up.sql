-- Migrate active Free subscribers to "Базовый" with a 30-day comp grace period,
-- then drop the Free plan entirely. Legacy Free plan rows kept only if they
-- still have inactive history rows pointing to them (FK).
DO $$
DECLARE
    free_id UUID;
    basic_id UUID;
BEGIN
    SELECT id INTO free_id FROM plans WHERE name = 'Free' LIMIT 1;
    SELECT id INTO basic_id FROM plans WHERE name = 'Базовый' LIMIT 1;

    IF free_id IS NULL THEN
        RAISE NOTICE 'Free plan not present, nothing to migrate';
        RETURN;
    END IF;

    IF basic_id IS NULL THEN
        RAISE EXCEPTION 'Базовый plan missing — migration 014 must run first';
    END IF;

    -- Active Free subs → Базовый, expires 30 days from now
    UPDATE subscriptions
    SET plan_id    = basic_id,
        expires_at = NOW() + INTERVAL '30 days'
    WHERE plan_id = free_id AND is_active = TRUE;

    -- Inactive Free subs: just retire them (history) by leaving as-is, but if
    -- safe (no active sub on Free at all), we can drop the plan row.
    IF NOT EXISTS (SELECT 1 FROM subscriptions WHERE plan_id = free_id) THEN
        DELETE FROM plans WHERE id = free_id;
    ELSE
        -- Keep Free row for FK integrity of historical inactive subs.
        RAISE NOTICE 'Free plan kept (referenced by inactive subscriptions)';
    END IF;
END $$;
