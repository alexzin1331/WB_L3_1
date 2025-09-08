-- тип для статусов
CREATE TYPE notify_status AS ENUM ('scheduled','sent','canceled','failed','retry');

-- таблица уведомлений
CREATE TABLE IF NOT EXISTS notifications (
    id SERIAL PRIMARY KEY,
    user_id TEXT,
    due_at TIMESTAMPTZ NOT NULL,
    message TEXT,
    status notify_status NOT NULL DEFAULT 'scheduled',
    attempt_count INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 5,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT
);

-- индексы для оптимизации выборки
CREATE INDEX IF NOT EXISTS idx_notifications_status_next ON notifications (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_notifications_due ON notifications (due_at);
