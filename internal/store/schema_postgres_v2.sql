CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_devices_registered_at_id ON devices(registered_at DESC, id DESC);
